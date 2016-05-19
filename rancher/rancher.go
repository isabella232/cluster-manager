package rancher

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	netUrl "net/url"
	"os"
	"path"
	"strings"
	"time"

	// not needed, working around goimports issue in vim
	_ "github.com/docker/machine/libmachine/log"
	"github.com/rancher/go-rancher/client"
)

const (
	projectUUIDBase = "system-ha-"
	systemSsl       = "system-ssl"
	agentImage      = "bootstrap.required.image"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func WaitForHosts(accessKey, secretKey, url string, count int) error {
	c, err := client.NewRancherClient(&client.ClientOpts{
		Url:       url,
		AccessKey: accessKey,
		SecretKey: secretKey,
	})
	if err != nil {
		return err
	}

	opts := client.NewListOpts()
	opts.Filters["state"] = "active"
	hosts, err := c.Host.List(opts)
	if err != nil {
		return err
	}

	for i := 0; len(hosts.Data) < count; i++ {
		if i > 30 {
			return errors.New("Timeout waiting for hosts to be active")
		}
		log.Infof("Waiting for %d host(s) to be active", count)
		time.Sleep(2 * time.Second)
		hosts, err = c.Host.List(opts)
		if err != nil {
			return err
		}
	}

	return nil
}

func GetRancherAgentImage(accessKey, secretKey, url string) (string, error) {
	c, err := client.NewRancherClient(&client.ClientOpts{
		Url:       url,
		AccessKey: accessKey,
		SecretKey: secretKey,
	})
	if err != nil {
		return "", err
	}

	opts := client.NewListOpts()
	opts.Filters["name"] = agentImage
	settings, err := c.Setting.List(opts)
	if err != nil {
		return "", err
	}

	for _, s := range settings.Data {
		if s.Name == agentImage {
			return s.ActiveValue, err
		}
	}

	return "", fmt.Errorf("Failed to find setting %s to determine agent image", agentImage)
}

func ConfigureEnvironment(create bool, configDir, cert, key, chain, accessKey, secretKey, url string, hostnames ...string) (string, string, error) {
	c, err := client.NewRancherClient(&client.ClientOpts{
		Url:       url,
		AccessKey: accessKey,
		SecretKey: secretKey,
	})
	if err != nil {
		return "", "", err
	}

	setting, err := c.Setting.ById("ha.enabled")
	if err != nil {
		return "", "", err
	}

	if setting.ActiveValue != "true" {
		return "", "", fmt.Errorf("HA is not enabled, ha.enabled=%s", setting.ActiveValue)
	}

	project, err := getProject(create, c)
	if err != nil {
		return "", "", err
	}

	if err := createCert(create, configDir, cert, key, chain, c, project, hostnames...); err != nil {
		return "", "", err
	}

	token, err := getToken(create, c, project)
	if err != nil {
		return "", "", err
	}

	urlObj, err := netUrl.Parse(url)
	if err != nil {
		return "", "", err
	}

	urlObj.Path = fmt.Sprintf("/v1/projects/%s/schemas", project.Id)
	return urlObj.String(), token, err
}

func getProject(create bool, c *client.RancherClient) (*client.Project, error) {
	opts := client.NewListOpts()
	opts.Filters["uuid_like"] = projectUUIDBase + "%"
	opts.Filters["removed_null"] = "true"
	projects, err := c.Account.List(opts)
	if err != nil {
		return nil, err
	}
	if len(projects.Data) > 0 {
		p, err := c.Project.ById(projects.Data[0].Id)
		return p, err
	}

	if create {
		uuid := fmt.Sprintf("%s%d", projectUUIDBase, rand.Int31())
		p, err := c.Project.Create(&client.Project{
			Uuid:            uuid,
			Name:            "System HA",
			Description:     "Management components",
			AllowSystemRole: true,
		})
		if err != nil {
			log.Infof("Failed to create project: %v", err)
			return nil, err
		}
		return p, nil
	}

	return nil, errors.New("HA environment has not yet been created")
}

func getToken(create bool, c *client.RancherClient, p *client.Project) (string, error) {
	opts := client.NewListOpts()
	opts.Filters["accountId"] = p.Id
	opts.Filters["removed_null"] = "1"
	rt, err := c.RegistrationToken.List(opts)
	if err != nil {
		return "", err
	}

	var token *client.RegistrationToken
	if len(rt.Data) == 0 {
		if !create {
			return "", errors.New("Registration token not yet created")
		}
		token, err = c.RegistrationToken.Create(&client.RegistrationToken{
			AccountId: p.Id,
		})
		if err != nil {
			return "", err
		}
	} else {
		token = &rt.Data[0]
	}

	err = WaitToken(c, token)
	if err != nil {
		return "", err
	}

	if token.State != "active" {
		return "", fmt.Errorf("Token is not active, in state [%s]", token.State)
	}

	return token.Token, err
}

func createCert(create bool, configPath, certPath, keyPath, chainPath string, c *client.RancherClient, p *client.Project,
	hostnames ...string) error {
	opts := client.NewListOpts()
	opts.Filters["name"] = systemSsl
	opts.Filters["removed_null"] = "1"
	opts.Filters["accountId"] = p.Id
	certs, err := c.Certificate.List(opts)
	if err != nil {
		return err
	}

	if len(certs.Data) > 0 {
		return saveCert(&certs.Data[0], configPath, chainPath)
	}

	if !create {
		return errors.New("Certificates not yet created")
	}

	cert, key, chain, err := GenerateCert(configPath, certPath, keyPath, chainPath, hostnames...)
	if err != nil {
		return err
	}

	newCert, err := c.Certificate.Create(&client.Certificate{
		AccountId:   p.Id,
		Name:        systemSsl,
		Description: "Certificate used for main load balancer",
		Cert:        cert,
		CertChain:   chain,
		Key:         key,
	})

	return saveCert(newCert, configPath, chainPath)
}

func saveCert(cert *client.Certificate, configPath, chainPath string) error {
	os.MkdirAll(configPath, 0755)
	certFile := path.Join(configPath, chainPath)
	certContent, err := ioutil.ReadFile(certFile)
	if os.IsNotExist(err) {
		return ioutil.WriteFile(certFile, []byte(cert.CertChain), 0644)
	} else if err != nil {
		return err
	} else if !strings.Contains(string(certContent), cert.CertChain) {
		f, err := os.OpenFile(certFile, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write([]byte("\n" + cert.CertChain))
		return err
	}

	return nil
}

func WaitForRancher(url string) bool {
	if !Ping(url) {
		log.Infof("Waiting for server to be available")
		return false
	}
	return true
}

func Ping(url string) bool {
	resp, err := http.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	buf := &bytes.Buffer{}
	_, err = io.Copy(buf, resp.Body)
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(buf.Bytes())) == "pong"
}
