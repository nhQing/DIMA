package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Container is one container on this machine, and the build it runs when
// DIMA can tell.
//
// "Can tell" is narrower than it looks. docker ps reports the image a
// container was started from by the name it was given — but only while that
// name still points at the same image. Once a later build moves the tag, the
// same container is reported by image ID instead. So a container whose Image
// equals a build's ref runs exactly the image that tag holds now, which is
// the newest successful build of it; one reported by ID runs something older
// and is honestly left without a version.
type Container struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`  // running, exited, ...
	Status string `json:"status"` // "Up 3 hours", "Exited (0) 2 days ago"
	Ports  string `json:"ports"`

	ProjectID string `json:"projectId,omitempty"`
	EnvID     string `json:"envId,omitempty"`
	BuildID   string `json:"buildId,omitempty"`
	Version   string `json:"version,omitempty"`
}

// readContainers runs docker ps. Tab separated for the same reason
// LocalImages is: the field names of docker's JSON output have changed
// between versions, the template names have not.
func readContainers(docker string) ([]Container, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, docker, "ps", "-a", "--no-trunc",
		"--format", "{{.ID}}\t{{.Names}}\t{{.Image}}\t{{.State}}\t{{.Status}}\t{{.Ports}}")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	list := []Container{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 5 || f[0] == "" {
			continue
		}
		c := Container{ID: f[0], Name: f[1], Image: f[2], State: f[3], Status: f[4]}
		if len(f) > 5 {
			c.Ports = f[5]
		}
		if len(c.ID) > 12 {
			c.ID = c.ID[:12]
		}
		list = append(list, c)
	}
	return list, nil
}

// matchContainers fills in which project, environment and build each
// container runs. history must be newest first, as Store.Builds returns it.
func matchContainers(cfg Config, history []Build, list []Container) []Container {
	byRef := map[string]Build{}
	byDigest := map[string]Build{}
	for _, b := range history {
		if b.Status != "success" {
			continue
		}
		if _, seen := byRef[b.Ref]; !seen && b.Ref != "" {
			byRef[b.Ref] = b
		}
		if b.Digest != "" {
			k := repoOf(b.Ref) + "@" + b.Digest
			if _, seen := byDigest[k]; !seen {
				byDigest[k] = b
			}
		}
	}
	// A repository a project ships, for containers whose tag DIMA never
	// recorded (latest, an ID): they still belong to the project.
	repoOwner := map[string]string{}
	for _, p := range cfg.Projects {
		for _, e := range p.Envs {
			repoOwner[p.Effective(e).Repo()] = p.ID
		}
	}

	out := make([]Container, 0, len(list))
	for _, c := range list {
		b, ok := byRef[c.Image]
		if !ok {
			b, ok = byDigest[c.Image]
		}
		if ok {
			c.ProjectID, c.EnvID, c.BuildID, c.Version = b.ProjectID, b.EnvID, b.ID, b.Version
		} else if owner, found := repoOwner[repoOf(c.Image)]; found {
			c.ProjectID = owner
		}
		out = append(out, c)
	}
	return out
}

// Containers is matchContainers over a fresh-enough docker ps. The state
// endpoint is polled every five seconds by every open window; ten seconds of
// caching keeps that from spawning a docker process per poll.
func (s *Store) Containers() []Container {
	const freshFor = 10 * time.Second
	s.mu.RLock()
	if time.Since(s.containersAt) < freshFor && s.containers != nil {
		list := s.containers
		s.mu.RUnlock()
		return matchContainers(s.Config(), s.Builds(), list)
	}
	s.mu.RUnlock()

	list, err := readContainers(s.Config().DockerBin)
	if err != nil {
		list = []Container{}
	}
	s.mu.Lock()
	s.containers, s.containersAt = list, time.Now()
	s.mu.Unlock()
	return matchContainers(s.Config(), s.Builds(), list)
}
