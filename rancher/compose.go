package rancher

import (
	"fmt"
	"os"
)
import "os/exec"

const (
	rancherCompose         = "rancher-compose"
	rancherComposeExecutor = "rancher-compose-executor"
)

func LaunchStack(env []string, accessKey, secretKey, url string) error {
	cmd := exec.Command(rancherCompose, "-p", "management", "-f", "compose/docker-compose.yml", "up", "-d", "-u", "-c")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = []string{
		fmt.Sprintf("RANCHER_URL=%s", url),
		fmt.Sprintf("RANCHER_ACCESS_KEY=%s", accessKey),
		fmt.Sprintf("RANCHER_SECRET_KEY=%s", secretKey)}
	cmd.Env = append(cmd.Env, env...)
	return cmd.Run()
}
