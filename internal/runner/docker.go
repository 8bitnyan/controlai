package runner

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerclient "github.com/docker/docker/client"
)

// DockerClient wraps the Docker engine SDK for the reconciler read path.
type DockerClient struct {
	c *dockerclient.Client
}

// NewDockerClient returns a DockerClient connected via the default socket.
func NewDockerClient() (*DockerClient, error) {
	c, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &DockerClient{c: c}, nil
}

// Close closes the underlying Docker client.
func (d *DockerClient) Close() error { return d.c.Close() }

// Ping verifies the Docker daemon is reachable.
func (d *DockerClient) Ping(ctx context.Context) error {
	_, err := d.c.Ping(ctx)
	return err
}

// ContainerSummary holds a subset of container fields used by the reconciler.
type ContainerSummary struct {
	ID          string
	Name        string
	ProjectID   string // value of com.docker.compose.project label
	State       string // "running" | "exited" | "dead" | etc.
	HealthStatus string // "healthy" | "unhealthy" | "" (no healthcheck)
}

// ListByProject returns all containers belonging to the given compose project.
func (d *DockerClient) ListByProject(ctx context.Context, projectID string) ([]ContainerSummary, error) {
	f := filters.NewArgs()
	f.Add("label", "com.docker.compose.project="+projectID)
	containers, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("list containers for %s: %w", projectID, err)
	}
	var out []ContainerSummary
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
		}
		cs := ContainerSummary{
			ID:        c.ID,
			Name:      name,
			ProjectID: c.Labels["com.docker.compose.project"],
			State:     c.State,
		}
		if c.Status != "" && c.State == "running" {
			cs.HealthStatus = parseHealthStatus(c.Status)
		}
		out = append(out, cs)
	}
	return out, nil
}

// ListAllControlaiContainers returns all containers with the controlai.tenant label.
func (d *DockerClient) ListAllControlaiContainers(ctx context.Context) ([]ContainerSummary, error) {
	f := filters.NewArgs()
	f.Add("label", "controlai.tenant")
	containers, err := d.c.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("list controlai containers: %w", err)
	}
	var out []ContainerSummary
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
		}
		out = append(out, ContainerSummary{
			ID:        c.ID,
			Name:      name,
			ProjectID: c.Labels["com.docker.compose.project"],
			State:     c.State,
		})
	}
	return out, nil
}

// InspectHealth returns the healthcheck status for a container.
func (d *DockerClient) InspectHealth(ctx context.Context, containerID string) (string, error) {
	info, err := d.c.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	if info.State != nil && info.State.Health != nil {
		return info.State.Health.Status, nil
	}
	return "", nil
}

// parseHealthStatus extracts the health status from docker ps Status string.
func parseHealthStatus(status string) string {
	// Status strings like "Up 5 minutes (healthy)" or "Up 2 hours (unhealthy)"
	if len(status) == 0 {
		return ""
	}
	if containsStr(status, "healthy") && !containsStr(status, "unhealthy") {
		return "healthy"
	}
	if containsStr(status, "unhealthy") {
		return "unhealthy"
	}
	return ""
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}


