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
	Ports           map[string]int

	SwarmEnabled bool
	HttpEnabled  bool

	ConfigPath          string
	CertPath            string
	KeyPath             string
	CertChainPath       string
	EncryptionKeyPath   string
	HostRegistrationUrl string
}

func (c *Config) LoadConfig() error {
	c.loadFromDocker()

	setFromEnv(&c.Image, "CATTLE_HA_CLUSTER_IMAGE")
	setFromEnv(&c.ClusterIP, "CATTLE_HA_CLUSTER_IP")
	setFromEnvInt(&c.ClusterSize, "CATTLE_HA_CLUSTER_SIZE")
	setFromEnv(&c.ContainerPrefix, "CATTLE_HA_CONTAINER_PREFIX")
	setFromEnv(&c.DockerSocket, "HOST_DOCKER_SOCK")

	setFromEnv(&c.DBHost, "CATTLE_DB_CATTLE_MYSQL_HOST")
	setFromEnvInt(&c.DBPort, "CATTLE_DB_CATTLE_MYSQL_PORT")
	setFromEnv(&c.DBName, "CATTLE_DB_CATTLE_MYSQL_NAME")
	setFromEnv(&c.DBUser, "CATTLE_DB_CATTLE_USERNAME")
	setFromEnv(&c.DBPassword, "CATTLE_DB_CATTLE_PASSWORD")

	setFromEnvBool(&c.SwarmEnabled, "CATTLE_HA_SWARM_ENABLED")
	setFromEnvBool(&c.HttpEnabled, "CATTLE_HA_HTTP_ENABLED")

	setFromEnv(&c.ConfigPath, "CATTLE_HA_CONFIG_PATH")
	setFromEnv(&c.CertPath, "CATTLE_HA_CERT_PATH")
	setFromEnv(&c.KeyPath, "CATTLE_HA_KEY_PATH")
	setFromEnv(&c.CertChainPath, "CATTLE_HA_CERT_CHAIN_PATH")
	setFromEnv(&c.EncryptionKeyPath, "CATTLE_HA_ENCRYPTION_KEY_PATH")
	setFromEnv(&c.HostRegistrationUrl, "CATTLE_HA_HOST_REGISTRATION_URL")

	if c.Ports == nil {
		c.Ports = map[string]int{}
	}

	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "CATTLE_HA_PORT_") {
			continue
		}
		keyValue := strings.SplitN(env, "=", 2)
		key := strings.TrimPrefix(keyValue[0], "CATTLE_HA_PORT_")
		key = strings.ToLower(key)
		key = strings.Replace(key, "_", "-", -1)
		value, err := strconv.Atoi(keyValue[1])
		if err != nil {
			return fmt.Errorf("Failed to read %s as an integer: %v", env, err)
		}
		c.Ports[key] = value
	}

	password, err := DecryptConfig(c, c.DBPassword)
	c.DBPassword = password
	return err
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

	if c.ContainerEnv == nil {
		c.ContainerEnv = map[string]string{}
	}

	if _, ok := c.ContainerEnv["CATTLE_HA_ENCRYPTION_KEY_PATH"]; !ok {
		c.ContainerEnv["CATTLE_HA_ENCRYPTION_KEY_PATH"] = c.EncryptionKeyPath
	}

	c.ContainerEnv["CATTLE_HA_CONTAINER"] = "true"
}

func setFromEnvBool(target *bool, key string) {
	val := os.Getenv(key)
	*target = strings.EqualFold(val, "true")
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

func (c *Config) ZkHost() string {
	return fmt.Sprintf("localhost:%d", db.ZkPortBaseClient)
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
