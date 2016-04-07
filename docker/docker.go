package docker

import (
	"bufio"
	"errors"
	"fmt"
	"io/ioutil"
	"math/big"
	"net"
	"os"
	"reflect"
	"regexp"
	"strings"

	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/docker/docker/pkg/term"
	"github.com/docker/docker/reference"
	"github.com/docker/engine-api/client"
	"github.com/docker/engine-api/types"
	"github.com/docker/engine-api/types/container"
	"github.com/docker/engine-api/types/filters"
	"github.com/docker/go-connections/nat"
	"github.com/rancher/cluster-manager/db"
)

const (
	ConfigDirDest = "/var/lib/rancher/etc"
)

var (
	log    = logrus.WithField("component", "docker")
	Parent = Container{
		Name:       "parent",
		Networking: true,
		Command:    []string{"parent"},
		Ports: []string{
			"18080:8080/tcp",
			"2181:12181/tcp",
			"2888:12888/tcp",
			"3888:13888/tcp",
			"6379:16379/tcp",
		},
		Labels: map[string]string{
			"io.rancher.container.network": "true",
		},
		DeleteLabeled: map[string]string{
			"io.rancher.ha.container": "true",
		},
		OpenStdin: true,
		RestartPolicy: container.RestartPolicy{
			Name: "always",
		},
	}
	cgroupPattern = regexp.MustCompile("^.*/docker-([a-z0-9]+).scope$")
)

type Docker struct {
	Cli        *client.Client
	configDir  string
	image      string
	prefix     string
	defaultEnv map[string]string
	portMap    map[string]int
}

type Container struct {
	Name          string
	Image         string
	Command       []string
	Env           map[string]string
	Labels        map[string]string
	DeleteLabeled map[string]string
	Networking    bool
	Ports         []string
	RestartPolicy container.RestartPolicy
	OpenStdin     bool
	Privileged    bool
	Volumes       map[string]string
	CheckRunning  string
}

func New(configDir, prefix, image string, portMap map[string]int, defaultEnv map[string]string) (*Docker, error) {
	defaultHeaders := map[string]string{"User-Agent": "engine-api-cli-1.0"}
	cli, err := client.NewClient("unix:///var/run/docker.sock", "v1.22", nil, defaultHeaders)
	return &Docker{
		configDir:  configDir,
		prefix:     prefix,
		Cli:        cli,
		image:      image,
		defaultEnv: defaultEnv,
		portMap:    portMap,
	}, err
}

func (d *Docker) Name() (string, error) {
	i, err := d.Cli.Info()
	return i.Name, err
}

func (d *Docker) getParent() Container {
	config := Parent
	for _, serviceName := range db.ServicePorts {
		port := db.DefaultServicePorts[serviceName]
		publicPort := db.LookupPortByService(d.portMap, serviceName)
		config.Ports = append(config.Ports, fmt.Sprintf("%d:1%d/tcp", publicPort, port))
	}
	return config
}

func (d *Docker) Launch(container Container) error {
	if !container.Networking {
		_, err := d.recreate(d.getParent())
		if err != nil {
			return err
		}
	}

	_, err := d.recreate(container)
	return err
}

func (d *Docker) Delete(name string) error {
	return d.Cli.ContainerRemove(types.ContainerRemoveOptions{
		ContainerID:   name,
		RemoveVolumes: true,
		Force:         true,
	})
}

func (d *Docker) GetBridgeIP() (string, error) {
	bridge, err := d.Cli.NetworkInspect("bridge")
	if err != nil {
		return "", err
	}

	if len(bridge.IPAM.Config) == 0 {
		return "", errors.New("Failed to find network address for bridge network")
	}

	ip, _, err := net.ParseCIDR(bridge.IPAM.Config[0].Subnet)
	if err != nil {
		return "", err
	}

	ipInt := big.NewInt(0)
	ipInt.SetBytes(ip.To4())
	ipInt.SetInt64(ipInt.Int64() + 1)
	b := ipInt.Bytes()
	return net.IPv4(b[0], b[1], b[2], b[3]).String(), nil
}

func (d *Docker) shouldDelete(container Container, c types.ContainerJSON) bool {
	changed := false
	if !reflect.DeepEqual([]string(c.Config.Cmd), container.Command) {
		log.Infof("Container %s command is different %v != %v", container.Name, c.Config.Cmd, container.Command)
		changed = true
	}

	for k, v := range container.Env {
		envStr := fmt.Sprintf("%s=%s", k, v)
		found := false
		for _, v := range c.Config.Env {
			if envStr == v {
				found = true
				break
			}
		}
		if !found {
			log.Infof("Container %s is missing env %s=%s", container.Name, k, v)
			changed = true
		}
	}

	if c.State == nil || !c.State.Running || c.State.Restarting {
		log.Infof("Container %s is not running in state %#v", container.Name, c.State)
		changed = true
	}

	return changed
}

func (d *Docker) deleteContainers(deleteLabels map[string]string) error {
	if len(deleteLabels) == 0 {
		return nil
	}

	labels := filters.NewArgs()
	for k, v := range deleteLabels {
		labels.Add("label", fmt.Sprintf("%s=%s", k, v))
	}
	cls, err := d.Cli.ContainerList(types.ContainerListOptions{
		Filter: labels,
	})
	if err != nil {
		return err
	}

	for _, toDelete := range cls {
		if err := d.deleteContainer(toDelete.ID); err != nil {
			return err
		}
	}

	return nil
}

func (d *Docker) deleteContainer(id string) error {
	log.Infof("Deleting container %s", id)
	return d.Cli.ContainerRemove(types.ContainerRemoveOptions{
		ContainerID:   id,
		RemoveVolumes: true,
		Force:         true,
	})
}

func (d *Docker) recreate(containerDef Container) (types.ContainerJSON, error) {
	if d.configDir != "" {
		if containerDef.Volumes == nil {
			containerDef.Volumes = map[string]string{}
		}
		containerDef.Volumes[d.configDir] = ConfigDirDest
	}

	c, err := d.Cli.ContainerInspect(d.prefix + containerDef.Name)
	if err != nil && !client.IsErrContainerNotFound(err) {
		return c, err
	}

	exists := (err == nil)
	if exists && d.shouldDelete(containerDef, c) {
		if err := d.deleteContainer(c.ID); err != nil {
			return c, err
		}
	} else if exists {
		return c, nil
	}

	if err := d.deleteContainers(containerDef.DeleteLabeled); err != nil {
		return c, err
	}

	if containerDef.CheckRunning != "" {
		check, err := d.Cli.ContainerInspect(containerDef.CheckRunning)
		if err == nil && check.State.Running && !check.State.Restarting {
			return c, nil
		}
	}

	config := container.Config{
		Cmd:          containerDef.Command,
		Env:          ToEnv(d.defaultEnv, containerDef.Env),
		ExposedPorts: map[nat.Port]struct{}{},
		Image:        containerDef.Image,
		OpenStdin:    containerDef.OpenStdin,
		Labels: map[string]string{
			"io.rancher.ha.container":    "true",
			"io.rancher.ha.service.name": containerDef.Name,
		},
		Volumes: map[string]struct{}{},
	}

	hostConfig := container.HostConfig{
		Privileged:    containerDef.Privileged,
		PortBindings:  nat.PortMap{},
		RestartPolicy: containerDef.RestartPolicy,
		Tmpfs: map[string]string{
			"/var/lib/zookeeper": "mode=0777",
			"/key":               "mode=0777",
		},
	}

	for k, v := range containerDef.Labels {
		config.Labels[k] = v
	}

	if config.Image == "" {
		config.Image = d.image
	}

	if containerDef.Networking {
		for _, port := range containerDef.Ports {
			if err := d.setPort(port, &config, &hostConfig); err != nil {
				return c, err
			}
		}
	} else {
		hostConfig.NetworkMode = container.NetworkMode(fmt.Sprintf("container:%s%s", d.prefix, Parent.Name))
	}

	for k, v := range containerDef.Volumes {
		config.Volumes[k] = struct{}{}
		hostConfig.Binds = append(hostConfig.Binds, fmt.Sprintf("%s:%s", k, v))
	}

	log.Infof("Creating container %s%s", d.prefix, containerDef.Name)
	resp, err := d.Cli.ContainerCreate(&config, &hostConfig, nil, d.prefix+containerDef.Name)
	if client.IsErrImageNotFound(err) {
		distributionRef, err := reference.ParseNamed(config.Image)
		if err != nil {
			return c, err
		}
		io, err := d.Cli.ImagePull(context.Background(), types.ImagePullOptions{
			ImageID: distributionRef.String(),
		}, func() (string, error) { return "", nil })
		if err != nil {
			return c, err
		}
		outFd, isTerminalOut := term.GetFdInfo(os.Stdout)
		if err := jsonmessage.DisplayJSONMessagesStream(io, os.Stdout, outFd, isTerminalOut, nil); err != nil {
			return c, err
		}
		resp, err = d.Cli.ContainerCreate(&config, &hostConfig, nil, d.prefix+containerDef.Name)
		if err != nil {
			return c, err
		}
	} else if err != nil {
		return c, err
	}

	err = d.Cli.ContainerStart(resp.ID)
	if err != nil {
		return c, err
	}

	return d.Cli.ContainerInspect(resp.ID)
}

func (d *Docker) setPort(portSpec string, config *container.Config, hostConfig *container.HostConfig) error {
	parts := strings.Split(portSpec, ":")
	config.ExposedPorts[nat.Port(parts[len(parts)-1])] = struct{}{}

	if len(parts) == 3 {
		if parts[0] == "BRIDGE" {
			bridgeIP, err := d.GetBridgeIP()
			if err != nil {
				return err
			}
			parts[0] = bridgeIP
		}

		hostConfig.PortBindings[nat.Port(parts[2])] = []nat.PortBinding{
			{
				HostIP:   parts[0],
				HostPort: parts[1],
			},
		}
	} else if len(parts) == 2 {
		hostConfig.PortBindings[nat.Port(parts[1])] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: parts[0],
			},
		}
	}

	return nil
}

func ToEnv(env ...map[string]string) []string {
	envs := []string{}
	for _, e := range env {
		for k, v := range e {
			envs = append(envs, strings.Join([]string{k, v}, "="))
		}
	}
	return envs
}

func ParseEnv(env []string) map[string]string {
	result := map[string]string{}
	for _, v := range env {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		} else {
			result[parts[0]] = ""
		}
	}
	return result
}

func findContainerID() (string, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/cgroup", os.Getpid()))
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "docker/") {
			parts := strings.Split(scanner.Text(), "/")
			return parts[len(parts)-1], nil
		}
		matches := cgroupPattern.FindAllStringSubmatch(scanner.Text(), -1)
		if len(matches) > 0 && len(matches[0]) > 1 && matches[0][1] != "" {
			return matches[0][1], nil
		}
	}

	content, _ := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cgroup", os.Getpid()))
	return "", fmt.Errorf("Failed to find container id:\n%s", string(content))
}

func GetImageAndEnv() (string, map[string]string, bool) {
	id, err := findContainerID()
	if err != nil {
		return "", nil, false
	}

	c, err := New("", "", "", nil, nil)
	container, err := c.Cli.ContainerInspect(id)
	if err != nil {
		return "", nil, false
	}

	return container.Config.Image, ParseEnv(container.Config.Env), true
}
