package dockertest

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cenk/backoff"
)

var (
	portRegex = regexp.MustCompile(`([0-9]+)\/(.+?)\s\->.+?:([0-9]+)`)
)

// Container is docker container instance.
type Container struct {
	containerID string
	image       string
	ports       map[int]int
	networks    map[int]string
	host        string
}

// Run image and returns docker container.
func Run(image string, args ...string) (*Container, error) {
	return RunEnvs(image, nil, args...)
}

// RunEnvs image with environment variables and returns docker container.
func RunEnvs(image string, envs map[string]string, args ...string) (*Container, error) {
	cmdargs := []string{"run", "-P", "-d"}

	// append environment variables
	for k, v := range envs {
		cmdargs = append(cmdargs, "-e", k+"="+v)
	}
	cmdargs = append(cmdargs, args...)
	cmdargs = append(cmdargs, image)

	// run and get containerID
	containerID, err := run("docker", cmdargs...)
	if err != nil {
		return nil, fmt.Errorf("failed run docker image:%s args:%v", image, args)
	}

	// get port map
	ports, err := run("docker", "port", containerID)
	if err != nil {
		return nil, fmt.Errorf("failed get ports image:%s", image)
	}

	host := "127.0.0.1"

	// for docker-machine
	if os.Getenv("DOCKER_HOST") != "" {
		host = os.Getenv("DOCKER_HOST")
	}

	c := &Container{
		containerID: containerID,
		image:       image,
		host:        host,
	}
	c.parsePorts(ports)
	return c, nil
}

// Close docker container.
func (c *Container) Close() error {
	if _, err := run("docker", "stop", c.containerID); err != nil {
		return err
	}
	// wait until docker stops ignoring the errors
	run("docker", "wait", c.containerID) // nolint: errcheck
	// remove the container
	_, err := run("docker", "rm", c.containerID)
	return err
}

// KillRemove kills and removes container.
func (c *Container) KillRemove() error {
	if _, err := run("docker", "kill", c.containerID); err != nil {
		return err
	}
	_, err := run("docker", "rm", c.containerID)
	return err
}

// Host returns host IP which runs docker.
func (c *Container) Host() string {
	return c.host
}

// WaitPort waits until port available.
func (c *Container) WaitPort(port int, timeout time.Duration) (int, error) {
	// wait until port available
	p := c.ports[port]
	if p == 0 {
		return 0, fmt.Errorf("port %d is not exposed on %s", port, c.image)
	}

	nw := c.networks[port]
	if nw == "" {
		return 0, fmt.Errorf("network not described on %s", c.image)
	}

	end := time.Now().Add(timeout)
	for {
		now := time.Now()
		_, err := net.DialTimeout(nw, c.Addr(port), end.Sub(now))
		if err != nil {
			if time.Now().After(end) {
				return 0, fmt.Errorf("port %d not available on %s for %f seconds", port, c.image, timeout.Seconds())
			}
			time.Sleep(time.Second)
			continue
		}
		break
	}
	return p, nil
}

// WaitHTTP waits until http available
func (c *Container) WaitHTTP(port int, path string, timeout time.Duration) (int, error) {
	p := c.ports[port]
	if p == 0 {
		return 0, fmt.Errorf("port %d is not exposed on %s", port, c.image)
	}
	now := time.Now()
	end := now.Add(timeout)
	for {
		cli := http.Client{Timeout: timeout}
		res, err := cli.Get("http://" + c.Addr(port) + path)
		if err != nil {
			if time.Now().After(end) {
				return 0, fmt.Errorf("http not available on port %d for %s err:%v", port, c.image, err)
			}
			// sleep 1 sec to retry
			time.Sleep(1 * time.Second)
			continue
		}
		defer res.Body.Close() // nolint: errcheck
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			if time.Now().After(end) {
				return 0, fmt.Errorf("http has not valid status code on port %d for %s code:%d", port, c.image, res.StatusCode)
			}
			// sleep 1 sec to retry
			time.Sleep(1 * time.Second)
			continue
		}
		break
	}
	return p, nil
}

// Wait is an exponential backoff retry. It waits until check function returns non error.
func (c *Container) Wait(maxInterval, maxWait time.Duration, check func() error) error {
	if maxWait == 0 {
		maxWait = time.Minute
	}
	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = maxInterval
	bo.MaxElapsedTime = maxWait
	return backoff.Retry(check, bo)
}

// Port returns exposed port in docker host.
func (c *Container) Port(port int) int {
	return c.ports[port]
}

// Addr returns exposed address like 127.0.0.1:6379.
func (c *Container) Addr(port int) string {
	exposed := c.Port(port)
	return net.JoinHostPort(c.host, strconv.Itoa(exposed))
}

// run command and get result.
func run(name string, args ...string) (out string, err error) {

	cmd := exec.Command(name, args...)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err = cmd.Run(); err != nil {
		return
	}

	if cmd.ProcessState.Success() {
		return strings.TrimSpace(stdout.String()), nil
	}

	err = errors.New("command execution failed " + stderr.String())
	return
}

func (c *Container) parsePorts(lines string) {

	matches := portRegex.FindAllStringSubmatch(lines, -1)
	c.ports = make(map[int]int, len(matches))
	c.networks = make(map[int]string, len(matches))

	for _, match := range matches {
		p1, _ := strconv.Atoi(match[1])
		p2, _ := strconv.Atoi(match[3])
		c.ports[p1] = p2
		c.networks[p1] = match[2]
	}

}
