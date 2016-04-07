package main

import (
	"github.com/Sirupsen/logrus"
	"github.com/rancher/cluster-manager/cluster"
	"github.com/rancher/cluster-manager/config"
	"github.com/satori/go.uuid"
)

func main() {
	c := &config.Config{
		UUID:              uuid.NewV4().String(),
		ContainerPrefix:   "rancher-ha-",
		ClusterSize:       3,
		DockerSocket:      "/var/run/docker.sock",
		DBUser:            "cattle",
		DBPassword:        "cattle",
		DBHost:            "mysql",
		DBPort:            3306,
		DBName:            "cattle",
		SwarmEnabled:      true,
		HTTPEnabled:       true,
		ConfigPath:        "/var/lib/rancher/etc",
		CertPath:          "server/cert.pem",
		KeyPath:           "server/key.pem",
		CertChainPath:     "server/ca.pem",
		EncryptionKeyPath: "server/encryption.key",
	}

	c.LoadConfig()

	if c.ClusterSize == 1 && c.ClusterIP == "" {
		c.ClusterIP = "127.0.0.1"
	}

	if c.ClusterIP == "" {
		logrus.Fatalf("CATTLE_HA_CLUSTER_IP must be set")
	}

	if err := c.OpenDB(); err != nil {
		logrus.WithField("err", err).Fatalf("Failed to create manager")
	}

	cluster, err := cluster.New(c)
	if err != nil {
		logrus.WithField("err", err).Fatalf("Failed to create manager")
	}

	if err := cluster.Start(); err != nil {
		logrus.WithField("err", err).Fatalf("Failed to create manager")
	}
}
