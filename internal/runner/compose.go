// Package runner wraps docker compose CLI invocations and the Docker SDK
// read path. All compose mutations go through this package so that the
// concurrency controls (per-project mutex + global semaphore) are enforced.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"golang.org/x/sync/semaphore"
)

// maxConcurrentCompose is the global semaphore weight cap (design D3).
const maxConcurrentCompose = 15

var (
	globalSem  = semaphore.NewWeighted(maxConcurrentCompose)
	projectMu  = sync.Map{} // project_id → *sync.Mutex
)

// projectMutex returns (or creates) the per-project mutex.
func projectMutex(projectID string) *sync.Mutex {
	v, _ := projectMu.LoadOrStore(projectID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// ComposeResult holds the stdout+stderr from a compose invocation.
type ComposeResult struct {
	Stdout string
	Stderr string
}

// Up runs `docker compose -p <project> -f <file> up -d --no-deps [services...]`.
// It acquires both the per-project mutex and the global semaphore.
func Up(ctx context.Context, projectID, composeFile string, services ...string) (ComposeResult, error) {
	return runCompose(ctx, projectID, composeFile, append([]string{"up", "-d", "--no-deps"}, services...)...)
}

// Down runs `docker compose -p <project> -f <file> down`.
func Down(ctx context.Context, projectID, composeFile string) (ComposeResult, error) {
	return runCompose(ctx, projectID, composeFile, "down")
}

// Restart runs `docker compose -p <project> -f <file> restart [services...]`.
func Restart(ctx context.Context, projectID, composeFile string, services ...string) (ComposeResult, error) {
	return runCompose(ctx, projectID, composeFile, append([]string{"restart"}, services...)...)
}

// PSService holds a single container status from `docker compose ps`.
type PSService struct {
	Name  string `json:"Name"`
	State string `json:"State"`
}

// PS runs `docker compose -p <project> -f <file> ps --format json` and parses
// the NDJSON output into a slice of PSService.
func PS(ctx context.Context, projectID, composeFile string) ([]PSService, error) {
	res, err := runCompose(ctx, projectID, composeFile, "ps", "--format", "json")
	if err != nil {
		return nil, err
	}
	var svcs []PSService
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		var s PSService
		if jsonErr := json.Unmarshal([]byte(line), &s); jsonErr == nil {
			svcs = append(svcs, s)
		}
	}
	return svcs, nil
}

// runCompose acquires the per-project mutex and global semaphore then shells
// out to `docker compose`.
func runCompose(ctx context.Context, projectID, composeFile string, args ...string) (ComposeResult, error) {
	mu := projectMutex(projectID)
	mu.Lock()
	defer mu.Unlock()

	if err := globalSem.Acquire(ctx, 1); err != nil {
		return ComposeResult{}, fmt.Errorf("acquire semaphore for %s: %w", projectID, err)
	}
	defer globalSem.Release(1)

	cmdArgs := append([]string{"compose", "-p", projectID, "-f", composeFile}, args...)
	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := ComposeResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		return res, fmt.Errorf("docker compose %s %s: %w (stderr: %s)",
			projectID, strings.Join(args, " "), err, stderr.String())
	}
	return res, nil
}
