package service

import (
	"errors"
	"fmt"
	netUrl "net/url"
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
		if len(newState.cluster) <= z.config.ClusterSize/2 {
			log.Infof("Waiting for at least %d cluster members", z.config.ClusterSize/2+1)
			return nil
		}
		log.Infof("Cluster changed, index=%d, members=[%s]", newState.index, strings.Join(newState.cluster, ", "))

		if err := z.configure(newState); err != nil {
			return err
		}

		if err := z.launchRancherServer(); err != nil {
			return err
		}

		z.state = newState
	}

	if err := z.launchRancherAgent(master); err != nil {
		log.Infof("Can not launch agent right now: %v", err)
		// Ensure that the server is running
		if err := z.launchRancherServer(); err != nil {
			return err
		}
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
		return fmt.Errorf("Server not available at %s:", pingURL)
	}

	hostnames := []string{}
	if z.config.HostRegistrationURL != "" {
		u, err := netUrl.Parse(z.config.HostRegistrationURL)
		if err == nil {
			hostnames = append(hostnames, u.Host)
		}
	}

	projectURL, token, err := rancher.ConfigureEnvironment(master, z.config.ConfigPath, z.config.CertPath, z.config.KeyPath,
		z.config.CertChainPath, accessKey, secretKey, url, hostnames...)
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
			"CATTLE_AGENT_IP":     z.config.ClusterIP,
			"CATTLE_URL_OVERRIDE": urlOverride,
		},
		CheckRunning: "rancher-agent",
	}); err != nil {
		return err
	}

	if !z.launchedStack && master {
		if err := rancher.WaitForHosts(accessKey, secretKey, projectURL, z.config.ClusterSize); err != nil {
			z.d.Delete("rancher-agent")
			log.Fatalf("Failed while waiting for %d host(s) to be active: %v", z.config.ClusterSize, err)
			return err
		}
		env := docker.ToEnv(map[string]string{
			"CATTLE_HA_PORT_PP_HTTP":  strconv.Itoa(db.LookupPortByService(z.config.Ports, db.PPHTTP)),
			"CATTLE_HA_PORT_PP_HTTPS": strconv.Itoa(db.LookupPortByService(z.config.Ports, db.PPHTTPS)),
			"CATTLE_HA_PORT_HTTP":     strconv.Itoa(db.LookupPortByService(z.config.Ports, db.HTTP)),
			"CATTLE_HA_PORT_HTTPS":    strconv.Itoa(db.LookupPortByService(z.config.Ports, db.HTTPS)),
			"CATTLE_HA_PORT_SWARM":    strconv.Itoa(db.LookupPortByService(z.config.Ports, db.Swarm)),
			"HA_IMAGE":                z.config.Image,
			"CONFIG_PATH":             z.config.ConfigPath,
		})
		if err := rancher.LaunchStack(env, accessKey, secretKey, projectURL); err != nil {
			return err
		}
		log.Infof("Done launching management stack")
		if z.config.HostRegistrationURL != "" {
			log.Infof("You can access the site at %s", z.config.HostRegistrationURL)
		}
		z.launchedStack = true
	}

	if !master {
		z.launchedStack = master
	}

	return nil
}

func (z *ClusterService) launchRancherServer() error {
	env := map[string]string{
		"CATTLE_SWARM_TLS_PORT":              strconv.Itoa(db.LookupPortByService(z.config.Ports, db.Swarm)),
		"CATTLE_MACHINE_EXECUTE":             "false",
		"CATTLE_COMPOSE_EXECUTOR_EXECUTE":    "false",
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
		"CATTLE_PROXY_PROTOCOL_HTTPS_PORTS":  strconv.Itoa(db.LookupPortByService(z.config.Ports, db.HTTPS)),
	}

	if z.config.HAEnabled {
		env["CATTLE_HA_ENABLED"] = "true"
	}

	if z.config.HostRegistrationURL != "" {
		env["DEFAULT_CATTLE_API_HOST"] = z.config.HostRegistrationURL
	}

	return z.d.Launch(docker.Container{
		Name:    "cattle",
		Command: []string{"cattle"},
		RestartPolicy: container.RestartPolicy{
			Name: "always",
		},
		Env: env,
	})
}

func (z *ClusterService) createTunnels(state clusterState) error {
	for i := 1; i <= z.config.ClusterSize; i++ {
		if _, ok := state.clusterByIndex[i]; !ok {
			z.tunnel.DeleteTunnels(i)
		}
	}

	for i := 1; i <= z.config.ClusterSize; i++ {
		target, ok := state.clusterByIndex[i]
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
			RestartPolicy: container.RestartPolicy{
				Name: "always",
			},
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
