package service

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/engine-api/client"
	"github.com/docker/engine-api/types/container"
	"github.com/rancher/cluster-manager/config"
	"github.com/rancher/cluster-manager/db"
	"github.com/rancher/cluster-manager/docker"
	"github.com/rancher/cluster-manager/rancher"
)

var (
	log = logrus.WithField("component", "service")
)

type clusterState struct {
	cluster        []string
	clusterByIndex map[int]db.Member
	index          int
}

type ClusterService struct {
	tunnel        *TunnelFactory
	config        *config.Config
	d             *docker.Docker
	state         clusterState
	launchedStack bool
}

func New(c *config.Config, d *docker.Docker) *ClusterService {
	return &ClusterService{
		config: c,
		d:      d,
		tunnel: NewTunnelFactory(c, d),
	}
}

func (z *ClusterService) Update(master bool, byIndex map[int]db.Member) error {
	newState := clusterState{
		cluster:        []string{},
		clusterByIndex: byIndex,
	}

	for i := 1; i <= z.config.ClusterSize; i++ {
		if byIndex[i].UUID == z.config.UUID {
			newState.index = i
		}
		newState.cluster = append(newState.cluster, byIndex[i].IP)
	}

	if z.state.index != newState.index || !reflect.DeepEqual(z.state.cluster, newState.cluster) {
		log.Infof("Cluster changed, index=%d, members=[%s]", newState.index, strings.Join(newState.cluster, ", "))
		if err := z.configure(newState); err != nil {
			return err
		}
		z.state = newState

		if len(newState.cluster) > z.config.ClusterSize/2 {
			if err := z.launchRancherServer(); err != nil {
				return err
			}

		}
	}

	if err := z.launchRancherAgent(master); err != nil {
		log.Infof("Can not launch agent right now: %v", err)
	}

	return nil
}

func (z *ClusterService) launchRancherAgent(master bool) error {
	accessKey, secretKey, err := z.config.APIKeys()
	if err != nil {
		return errors.New("Waiting for server to create service API key")
	}

	bridgeIP, err := z.d.GetBridgeIP()
	if err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s:%d/v1/schemas", bridgeIP, db.RancherServerPort)
	pingURL := fmt.Sprintf("http://%s:%d/ping", bridgeIP, db.RancherServerPort)

	if !rancher.WaitForRancher(pingURL) {
		return errors.New("Server not available")
	}

	projectURL, token, err := rancher.ConfigureEnvironment(accessKey, secretKey, url)
	if err != nil {
		return err
	}

	agentImage, err := rancher.GetRancherAgentImage(accessKey, secretKey, url)
	if err != nil {
		return err
	}

	tokenURL := fmt.Sprintf("http://%s:%d/v1/scripts/%s", bridgeIP, db.RancherServerPort, token)
	urlOverride := fmt.Sprintf("http://%s:%d/v1", bridgeIP, db.RancherServerPort)
	if err := z.d.Launch(docker.Container{
		Name:       "agent",
		Image:      agentImage,
		Privileged: true,
		Networking: true,
		Volumes: map[string]string{
			z.config.DockerSocket: "/var/run/docker.sock",
		},
		Command: []string{tokenURL},
		Env: map[string]string{
			"CATTLE_SCRIPT_DEBUG": "true",
			"CATTLE_AGENT_IP":     z.config.ClusterIP,
			"CATTLE_URL_OVERRIDE": urlOverride,
		},
		CheckRunning: "rancher-agent",
	}); err != nil {
		return err
	}

	if !z.launchedStack && master {
		env := docker.ToEnv(map[string]string{
			"HA_IMAGE": z.config.Image,
		})
		if err := rancher.LaunchStack(env, accessKey, secretKey, projectURL); err != nil {
			return err
		}
		z.launchedStack = true
	}

	return nil
}

func (z *ClusterService) launchRancherServer() error {
	return z.d.Launch(docker.Container{
		Name:    "cattle",
		Command: []string{"cattle"},
		RestartPolicy: container.RestartPolicy{
			Name: "always",
		},
		Env: map[string]string{
			"CATTLE_HOST_API_PROXY_MODE":         "ha",
			"CATTLE_MODULE_PROFILE_REDIS":        "true",
			"CATTLE_REDIS_HOSTS":                 z.config.RedisHosts(),
			"CATTLE_MODULE_PROFILE_ZOOKEEPER":    "true",
			"CATTLE_ZOOKEEPER_CONNECTION_STRING": z.config.ZkHosts(),
			"CATTLE_DB_CATTLE_DATABASE":          "mysql",
			"CATTLE_DB_CATTLE_MYSQL_HOST":        z.config.DBHost,
			"CATTLE_DB_CATTLE_MYSQL_PORT":        strconv.Itoa(z.config.DBPort),
			"CATTLE_DB_CATTLE_USERNAME":          z.config.DBUser,
			"CATTLE_DB_CATTLE_PASSWORD":          z.config.DBPassword,
			"CATTLE_DB_CATTLE_MYSQL_NAME":        z.config.DBName,
		}})
}

func (z *ClusterService) createTunnels(state clusterState) error {
	for i := 1; i <= z.config.ClusterSize; i++ {
		if _, ok := z.state.clusterByIndex[i]; !ok {
			z.tunnel.DeleteTunnels(i)
		}
	}

	for i := 1; i <= z.config.ClusterSize; i++ {
		target, ok := z.state.clusterByIndex[i]
		if !ok {
			continue
		}

		if err := z.tunnel.CreateTunnels(state.index != i, target); err != nil {
			return err
		}
	}

	return nil
}

func (z *ClusterService) configure(state clusterState) error {
	if err := z.createTunnels(state); err != nil {
		return err
	}

	if state.index <= 0 {
		return nil
	}

	for _, service := range []string{db.Zk, db.Redis} {
		if err := z.d.Launch(docker.Container{
			Name:    service,
			Command: []string{service},
			Env: map[string]string{
				"INDEX":        strconv.Itoa(state.index),
				"CLUSTER_SIZE": strconv.Itoa(z.config.ClusterSize),
			}}); err != nil {
			return err
		}
	}

	return nil
}

func (z *ClusterService) RequestedIndex() (int, error) {
	c, err := z.d.Cli.ContainerInspect(z.config.ContainerPrefix + docker.Parent.Name)
	if client.IsErrContainerNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	id := docker.ParseEnv(c.Config.Env)["INDEX"]
	if id == "" {
		return 0, nil
	}

	idNum, err := strconv.Atoi(id)
	if err != nil {
		return 0, nil
	}
	return idNum, nil
}
