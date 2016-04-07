package rancher

import (
	"io/ioutil"
	"os"
	"path"

	"github.com/Sirupsen/logrus"
	"github.com/docker/machine/libmachine/cert"
)

const (
	name string = "rancher"
	bits int    = 2048
)

var (
	log = logrus.WithField("component", "cert")
)

func GenerateCert(configPath, certPath, keyPath, chainPath string) (string, string, string, error) {
	cert, err := ioutil.ReadFile(path.Join(configPath, certPath))
	if os.IsNotExist(err) {
		return generateCert()
	}
	if err != nil {
		return "", "", "", err
	}

	key, err := ioutil.ReadFile(path.Join(configPath, keyPath))
	if err != nil {
		return "", "", "", err
	}

	chain, err := ioutil.ReadFile(path.Join(configPath, chainPath))
	if err != nil {
		return "", "", "", err
	}

	return string(cert), string(key), string(chain), nil
}

func generateCert() (string, string, string, error) {
	caCert, err := ioutil.TempFile("/tmp", "cacert")
	if err != nil {
		return "", "", "", err
	}
	caCert.Close()
	defer os.Remove(caCert.Name())

	caKey, err := ioutil.TempFile("/tmp", "cakey")
	if err != nil {
		return "", "", "", err
	}
	caKey.Close()
	defer os.Remove(caKey.Name())

	certFile, err := ioutil.TempFile("/tmp", "cert")
	if err != nil {
		return "", "", "", err
	}
	certFile.Close()
	defer os.Remove(certFile.Name())

	key, err := ioutil.TempFile("/tmp", "key")
	if err != nil {
		return "", "", "", err
	}
	key.Close()
	defer os.Remove(key.Name())

	log.Infof("Generating CA TLS certs")
	if err := cert.GenerateCACertificate(caCert.Name(), caKey.Name(), name, bits); err != nil {
		return "", "", "", err
	}

	log.Infof("Generating TLS certs")
	if err := cert.GenerateCert([]string{"localhost", "rancher"}, certFile.Name(), key.Name(), caCert.Name(), caKey.Name(), name, bits); err != nil {
		return "", "", "", err
	}

	content := []string{}
	for _, f := range []string{certFile.Name(), key.Name(), caCert.Name()} {
		bytes, err := ioutil.ReadFile(f)
		if err != nil {
			return "", "", "", err
		}
		content = append(content, string(bytes))
	}

	return content[0], content[1], content[2], nil
}
