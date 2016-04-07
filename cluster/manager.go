package cluster

import (
	"sort"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/rancher/cluster-manager/config"
	"github.com/rancher/cluster-manager/db"
	"github.com/rancher/cluster-manager/docker"
	"github.com/rancher/cluster-manager/service"
)

var (
	interval  = 5 * time.Second
	log       = logrus.WithField("component", "manager")
	maxMissed = 2
)

type Manager struct {
	db.Member
	config   *config.Config
	services *service.ClusterService
}

func New(config *config.Config) (*Manager, error) {
	d, err := docker.New(config.ConfigPath, config.ContainerPrefix, config.Image, config.Ports, config.ContainerEnv)
	if err != nil {
		return nil, err
	}

	services := service.New(config, d)
	requestedIndex, err := services.RequestedIndex()
	if err != nil {
		return nil, err
	}

	name, err := d.Name()
	if err != nil {
		return nil, err
	}

	m := &Manager{
		Member: db.Member{
			Name:           name,
			UUID:           config.UUID,
			IP:             config.ClusterIP,
			RequestedIndex: requestedIndex,
			Ports:          config.Ports,
		},
		config:   config,
		services: services,
	}

	return m, nil
}

func (m *Manager) Start() error {
	id, err := m.services.RequestedIndex()
	if err != nil {
		return err
	}

	m.RequestedIndex = id

	m.checkin(0)
	go m.heartbeat()
	return m.loop()
}

func (m *Manager) checkin(i int) {
	if err := m.config.DB.Checkin(m.Member, i); err != nil {
		log.WithField("err", err).Fatal("Failed to do cluster check in")
	}
}

type seen struct {
	member    db.Member
	heartbeat int
	missed    int
}

func (m *Manager) updateMembers(members map[string]*seen) error {
	for _, h := range members {
		h.missed++
	}

	newMembers, err := m.config.DB.Members()
	if err != nil {
		return err
	}

	for _, member := range newMembers {
		seenMember := members[member.UUID]
		if seenMember == nil {
			members[member.UUID] = &seen{
				member:    member,
				heartbeat: member.Heartbeat,
			}
		} else {
			seenMember.member = member
			if seenMember.heartbeat != member.Heartbeat {
				seenMember.missed = 0
				seenMember.heartbeat = member.Heartbeat
			}
		}
	}

	return nil
}

func (m *Manager) pruneMembers(members map[string]*seen) error {
	for key, seen := range members {
		if seen.missed >= maxMissed {
			log.WithField("member", seen.member).Info("Forgetting cluster member")
			err := m.config.DB.Delete(key)
			if err != nil {
				log.WithFields(logrus.Fields{"err": err, "member": seen.member}).Errorf("Failed to delete member")
			} else {
				delete(members, key)
			}
		}
	}

	return nil
}

func (m *Manager) isMaster(members map[string]*seen) bool {
	master := members[m.UUID]
	if master == nil {
		return false
	}

	for _, member := range members {
		if member.member.ID < master.member.ID {
			master = member
		}
	}

	return master.member.UUID == m.UUID
}

func (m *Manager) assignIndex(oldMembers map[string]*seen) (bool, error) {
	changed := false
	sortedKeys := []int{}
	byIndex := map[int]db.Member{}
	members := map[int]db.Member{}

	for _, v := range oldMembers {
		member := v.member
		if member.Index > 0 {
			byIndex[member.Index] = member
		} else {
			sortedKeys = append(sortedKeys, member.ID)
			members[member.ID] = member
		}
	}
	sort.Sort(sort.IntSlice(sortedKeys))

	// Assign my requested
	for _, key := range sortedKeys {
		member, ok := members[key]
		if !ok {
			continue
		}

		if member.RequestedIndex <= 0 {
			continue
		}

		if _, ok := byIndex[member.RequestedIndex]; !ok {
			log.Infof("Assigning %s %s to index %d by request", member.UUID, member.IP, member.RequestedIndex)
			changed = true
			byIndex[member.RequestedIndex] = member
			delete(members, key)
		}
	}

	// Assign my missing index
	for i := 1; i <= m.config.ClusterSize; i++ {
		if _, ok := byIndex[i]; ok {
			continue
		}

		for _, member := range members {
			log.Infof("Assigning %s %s to index %d", member.UUID, member.IP, i)
			changed = true
			byIndex[i] = member
			delete(members, member.ID)
			break
		}
	}

	if changed {
		if err := m.config.DB.SaveIndex(byIndex); err != nil {
			return false, err
		}
	}

	return changed, nil
}

func (m *Manager) loop() error {
	members := map[string]*seen{}
	master := false
	for ; ; time.Sleep(interval) {
		if err := m.updateMembers(members); err != nil {
			return err
		}

		if err := m.pruneMembers(members); err != nil {
			return err
		}

		newValue := m.isMaster(members)
		if newValue != master {
			log.WithField("master", newValue).Infof("Currently Master: %t", newValue)
		}
		master = newValue

		if master {
			if changed, err := m.assignIndex(members); err != nil {
				return err
			} else if changed {
				continue
			}
		}

		byIndex := sortByIndex(members)

		if err := m.services.Update(master, byIndex); err != nil {
			return err
		}
	}
}

func sortByIndex(members map[string]*seen) map[int]db.Member {
	result := map[int]db.Member{}
	for _, member := range members {
		if member.member.Index > 0 {
			result[member.member.Index] = member.member
		}
	}
	return result
}

func (m *Manager) heartbeat() {
	for i := 1; ; i++ {
		m.checkin(i)
		time.Sleep(interval)
	}
}
