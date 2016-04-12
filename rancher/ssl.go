package rancher

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io/ioutil"
	"math/big"
	"net"
	"os"
	"path"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/machine/libmachine/cert"
)

const (
	name string = "cattle"
	bits int    = 2048
)

var (
	log = logrus.WithField("component", "cert")
)

func GenerateCert(configPath, certPath, keyPath, chainPath string, hostnames ...string) (string, string, string, error) {
	cert, err := ioutil.ReadFile(path.Join(configPath, certPath))
	if os.IsNotExist(err) {
		return generateCert(hostnames...)
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

func generateCert(hostnames ...string) (string, string, string, error) {
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
	if err := generateCert2(append([]string{"localhost", "rancher"}, hostnames...), certFile.Name(), key.Name(), caCert.Name(), caKey.Name(), name, bits); err != nil {
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

// Copied from Docker Machine because we needed to set the SAN IP as a DNSName and IPAddress

func generateCert2(hosts []string, certFile, keyFile, caFile, caKeyFile, org string, bits int) error {
	template, err := newCertificate(org)
	if err != nil {
		return err
	}
	// client
	if len(hosts) == 1 && hosts[0] == "" {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		template.KeyUsage = x509.KeyUsageDigitalSignature
	} else { // server
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
		for _, h := range hosts {
			if ip := net.ParseIP(h); ip != nil {
				template.IPAddresses = append(template.IPAddresses, ip)
			}
			template.DNSNames = append(template.DNSNames, h)
		}
	}

	tlsCert, err := tls.LoadX509KeyPair(caFile, caKeyFile)
	if err != nil {
		return err
	}

	priv, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return err
	}

	x509Cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return err
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, x509Cert, &priv.PublicKey, tlsCert.PrivateKey)
	if err != nil {
		return err
	}

	certOut, err := os.Create(certFile)
	if err != nil {
		return err
	}

	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	certOut.Close()

	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}

	pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	keyOut.Close()

	return nil
}

func newCertificate(org string) (*x509.Certificate, error) {
	now := time.Now()
	// need to set notBefore slightly in the past to account for time
	// skew in the VMs otherwise the certs sometimes are not yet valid
	notBefore := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute()-5, 0, 0, time.Local)
	notAfter := notBefore.Add(time.Hour * 24 * 1080)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, err
	}

	return &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{org},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement,
		BasicConstraintsValid: true,
	}, nil

}
