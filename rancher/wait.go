package rancher

import (
	"time"

	"github.com/rancher/go-rancher/client"
)

func WaitFor(c *client.RancherClient, resource *client.Resource, output interface{}, transitioning func() string) error {
	for {
		if transitioning() != "yes" {
			return nil
		}

		time.Sleep(150 * time.Millisecond)

		err := c.Reload(resource, output)
		if err != nil {
			return err
		}
	}
}

func WaitToken(c *client.RancherClient, service *client.RegistrationToken) error {
	return WaitFor(c, &service.Resource, service, func() string {
		return service.Transitioning
	})
}
