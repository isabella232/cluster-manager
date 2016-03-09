package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/go-sql-driver/mysql"
	"github.com/rancher/cluster-manager/db"
	"github.com/rancher/cluster-manager/docker"
)

type Config struct {
	Image           string
	ClusterIP       string
	ClusterSize     int
	ContainerPrefix string
	ContainerEnv    map[string]string
	DockerSocket    string
	DB              *db.DB
	DBHost          string
	DBName          string
	DBPassword      string
	DBPort          int
	DBUser          string
	UUID            string
}

func (c *Config) LoadConfig() {
	c.loadFromDocker()

	setFromEnv(&c.Image, "HA_CLUSTER_IMAGE")
	setFromEnv(&c.ClusterIP, "HA_CLUSTER_IP")
	setFromEnvInt(&c.ClusterSize, "HA_CLUSTER_SIZE")
	setFromEnv(&c.ContainerPrefix, "HA_CONTAINER_PREFIX")
	setFromEnv(&c.DockerSocket, "HOST_DOCKER_SOCK")
	setFromEnv(&c.DBHost, "CATTLE_DB_CATTLE_MYSQL_HOST")
	setFromEnvInt(&c.DBPort, "CATTLE_DB_CATTLE_MYSQL_PORT")
	setFromEnv(&c.DBName, "CATTLE_DB_CATTLE_MYSQL_NAME")
	setFromEnv(&c.DBUser, "CATTLE_DB_CATTLE_USERNAME")
	setFromEnvInt(&c.DBPort, "CATTLE_DB_CATTLE_PASSWORD")
}

func (c *Config) loadFromDocker() {
	image, env, ok := docker.GetImageAndEnv()
	if ok {
		c.Image = image
		c.ContainerEnv = env
		for k := range env {
			switch {
			case k == "PATH":
				delete(env, k)
			case strings.Contains(k, "CATTLE_DB"):
				delete(env, k)
			}
		}
	}
}

func setFromEnvInt(target *int, key string) {
	val := os.Getenv(key)
	if val != "" {
		s, err := strconv.Atoi(val)
		if err != nil {
			logrus.Fatalf("%s must be a number, got %s", key, val)
		}
		*target = s
	}
}

func setFromEnv(target *string, key string) {
	val := os.Getenv(key)
	if val != "" {
		*target = val
	}
}

func (c *Config) ZkHosts() string {
	hosts := []string{}
	for i := 0; i < c.ClusterSize; i++ {
		hosts = append(hosts, fmt.Sprintf("localhost:%d", db.ZkPortBaseClient+i))
	}
	return strings.Join(hosts, ",")
}

func (c *Config) RedisHosts() string {
	hosts := []string{}
	for i := 0; i < c.ClusterSize; i++ {
		hosts = append(hosts, fmt.Sprintf("localhost:%d", db.RedisPortBase+i))
	}
	return strings.Join(hosts, ",")
}

func (c *Config) APIKeys() (string, string, error) {
	return c.DB.APIKeys()
}

func (c *Config) OpenDB() error {
	dsn := mysql.Config{
		User:      c.DBUser,
		Passwd:    c.DBPassword,
		Net:       "tcp",
		Addr:      fmt.Sprintf("%s:%d", c.DBHost, c.DBPort),
		DBName:    c.DBName,
		Collation: "utf8_general_ci",
	}

	dbDef, err := db.New("mysql", dsn.FormatDSN())
	if err != nil {
		return err
	}

	c.DB = dbDef

	return c.DB.Migrate()
}
