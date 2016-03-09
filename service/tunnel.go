package service

import (
	"fmt"

	"github.com/rancher/cluster-manager/config"
	"github.com/rancher/cluster-manager/db"
	"github.com/rancher/cluster-manager/docker"
)

var (
	services      = []string{db.Zk, db.Zk2, db.ZkClient, db.Redis}
	serviceToPort = map[string]int{
		db.Zk:       db.ZkPortBase,
		db.Zk2:      db.ZkPortBase2,
		db.ZkClient: db.ZkPortBaseClient,
		db.Redis:    db.RedisPortBase,
	}
)

type TunnelFactory struct {
	c *config.Config
	d *docker.Docker
}

func NewTunnelFactory(c *config.Config, d *docker.Docker) *TunnelFactory {
	return &TunnelFactory{
		c: c,
		d: d,
	}
}

func (t *TunnelFactory) DeleteTunnels(index int) error {
	var lastErr error
	for _, service := range services {
		err := t.deletePipe(service, index)
		if err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (t *TunnelFactory) CreateTunnels(outgoing bool, target db.Member) error {
	for _, service := range services {
		basePort := serviceToPort[service]

		if outgoing && target.IP == t.c.ClusterIP {
			// Don't encrypt back to yourself
			outgoing = false
		}

		if outgoing {
			if err := t.pipeEncrypt(service, target.Index, basePort, target.PortByService(service), target.IP); err != nil {
				return err
			}
		} else {
			if err := t.pipeDecrypt(service, target.Index, basePort, target.PortByService(service)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *TunnelFactory) pipeDecrypt(name string, index, basePort, port int) error {
	to := basePort + index - 1
	containerName := fmt.Sprintf("tunnel-%s-%d", name, index)
	source := fmt.Sprintf("[0.0.0.0]:%d", port+10000)
	target := fmt.Sprintf("[127.0.0.1]:%d", to)
	cmd := []string{"spiped", "-F", "-d", "-s", source, "-t", target, "-k", "keyfile"}

	return t.d.Launch(docker.Container{
		Name:    containerName,
		Command: cmd,
		Labels: map[string]string{
			"io.rancher.ha.service.tunnel": fmt.Sprintf("%s-%d", name, index),
		},
	})
}

func (t *TunnelFactory) pipeEncrypt(name string, index, basePort, port int, ip string) error {
	from := basePort + index - 1
	containerName := fmt.Sprintf("tunnel-%s-%d", name, index)
	source := fmt.Sprintf("[127.0.0.1]:%d", from)
	target := fmt.Sprintf("[%s]:%d", ip, port)
	cmd := []string{"spiped", "-F", "-e", "-s", source, "-t", target, "-k", "keyfile"}

	return t.d.Launch(docker.Container{
		Name:    containerName,
		Command: cmd,
		Labels: map[string]string{
			"io.rancher.ha.service.tunnel": fmt.Sprintf("%s-%d", name, index),
		},
	})
}

func (t *TunnelFactory) deletePipe(name string, index int) error {
	containerName := fmt.Sprintf("rancher-ha-tunnel-%s-%d", name, index)
	return t.d.Delete(containerName)
}
