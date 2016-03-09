package rancher

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	netUrl "net/url"
	"strings"

	// not needed, working around goimports issue in vim
	_ "github.com/docker/machine/libmachine/log"
	"github.com/rancher/go-rancher/client"
)

const (
	projectUUIDBase = "system-management"
	systemSsl       = "system-ssl"
	agentImage      = "bootstrap.required.image"
)

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
			return s.Value, err
		}
	}

	return "", fmt.Errorf("Failed to find setting %s to determine agent image", agentImage)
}

func ConfigureEnvironment(accessKey, secretKey, url string) (string, string, error) {
	c, err := client.NewRancherClient(&client.ClientOpts{
		Url:       url,
		AccessKey: accessKey,
		SecretKey: secretKey,
	})
	if err != nil {
		return "", "", err
	}

	project, _, err := getProject(c)
	if err != nil {
		return "", "", err
	}

	if err := createCert(c, project); err != nil {
		return "", "", err
	}

	token, err := getToken(c, project)
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

func getProject(c *client.RancherClient) (*client.Project, string, error) {
	for i := 0; ; i++ {
		opts := client.NewListOpts()
		uuid := fmt.Sprintf("%s%d", projectUUIDBase, i)
		opts.Filters["uuid"] = uuid
		projects, err := c.Project.List(opts)
		if err != nil {
			return nil, "", err
		}
		if len(projects.Data) == 0 {
			p, err := c.Project.Create(&client.Project{
				Uuid:            uuid,
				Name:            "System Management",
				Description:     "Management components",
				AllowSystemRole: true,
			})
			if err != nil {
				continue
			}
			return p, uuid, nil
		}
		for _, project := range projects.Data {
			if project.Removed == "" {
				return &project, uuid, nil
			}
		}
	}
}

func getToken(c *client.RancherClient, p *client.Project) (string, error) {
	opts := client.NewListOpts()
	opts.Filters["accountId"] = p.Id
	opts.Filters["removed_null"] = "1"
	rt, err := c.RegistrationToken.List(opts)
	if err != nil {
		return "", err
	}

	var token *client.RegistrationToken
	if len(rt.Data) == 0 {
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

func createCert(c *client.RancherClient, p *client.Project) error {
	opts := client.NewListOpts()
	opts.Filters["name"] = systemSsl
	opts.Filters["removed_null"] = "1"
	opts.Filters["accountId"] = p.Id
	certs, err := c.Certificate.List(opts)
	if err != nil {
		return err
	}

	if len(certs.Data) > 0 {
		return nil
	}

	cert, key, chain, err := GenerateCert()
	if err != nil {
		return err
	}

	_, err = c.Certificate.Create(&client.Certificate{
		AccountId:   p.Id,
		Name:        systemSsl,
		Description: "Certificate used for main load balancer",
		Cert:        cert,
		CertChain:   chain,
		Key:         key,
	})
	return err
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
