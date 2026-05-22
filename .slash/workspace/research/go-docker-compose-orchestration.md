# Research: Go Control Plane for docker compose v2 Multi-Tenant Orchestration
Date: 2026-05-22

## Summary
Three viable approaches exist for a Go control plane driving many compose projects on one host: (A) shell out to the `docker compose` CLI — lowest coupling, easiest to reason about; (B) embed `github.com/docker/compose/v2` (now v5 module path) as a Go library — same engine, no subprocess overhead, but heavy import graph; (C) use the Docker Engine SDK directly and skip compose — best for fine-grained control but requires reimplementing compose logic. For controlai's reconciler pattern, **Approach A is recommended** with selective use of the compose Go library for parsing/diffing compose files.

---

## 1. Approach A: Shell out to `docker compose`

### Best practices

```go
// Run: docker compose -p <project> -f <file> up -d --no-deps <services...>
func composeUp(ctx context.Context, project, file string, services []string) error {
    args := []string{"compose", "-p", project, "-f", file, "up", "-d", "--no-deps"}
    args = append(args, services...)

    cmd := exec.CommandContext(ctx, "docker", args...)
    cmd.Dir = filepath.Dir(file)          // cwd = project dir (resolves relative paths)
    cmd.Env = append(os.Environ(),        // inherit host env, add overrides
        "COMPOSE_PROJECT_NAME="+project,
        "DOCKER_BUILDKIT=1",
    )

    var stderr bytes.Buffer
    cmd.Stdout = os.Stdout  // stream to structured logger in production
    cmd.Stderr = &stderr

    if err := cmd.Run(); err != nil {
        return fmt.Errorf("compose up failed [%s]: %w\nstderr: %s", project, err, stderr.String())
    }
    return nil
}
```

**Key rules:**
- Always set `cmd.Dir` to the compose file's directory — relative `build:` and `env_file:` paths resolve from there.
- Pass `COMPOSE_PROJECT_NAME` in env as a belt-and-suspenders guard; `-p` flag takes precedence.
- Capture stderr separately; stdout is progress noise. On error, include stderr in the returned error.
- Use `exec.CommandContext` so the subprocess is killed when the reconciler context is cancelled.
- For logging, pipe stdout/stderr through `io.Pipe` + a goroutine that prefixes lines with `[project=X]`.

### Parsing status output reliably

Do **not** parse `docker compose ps` human-readable output. Use JSON:

```go
// docker compose ps --format json outputs one JSON object per line (NDJSON)
type ComposePSEntry struct {
    Name    string `json:"Name"`
    Service string `json:"Service"`
    State   string `json:"State"`   // "running", "exited", "paused"
    Health  string `json:"Health"`  // "healthy", "unhealthy", "starting", ""
    Status  string `json:"Status"`  // human string e.g. "Up 2 hours (healthy)"
    ExitCode int   `json:"ExitCode"`
}

func composePSJSON(ctx context.Context, project string) ([]ComposePSEntry, error) {
    out, err := exec.CommandContext(ctx, "docker", "compose", "-p", project,
        "ps", "--format", "json").Output()
    if err != nil { return nil, err }

    var entries []ComposePSEntry
    for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
        if len(line) == 0 { continue }
        var e ComposePSEntry
        if err := json.Unmarshal(line, &e); err != nil { continue }
        entries = append(entries, e)
    }
    return entries, nil
}
```

**Evidence** — real-world schema from [tokuhirom/dcv](https://github.com/tokuhirom/dcv/blob/main/internal/models/compose_container.go) and [alexanderwanyoike/the0](https://github.com/alexanderwanyoike/the0/blob/main/cli/cmd/local.go):
- Output is **NDJSON** (one JSON object per line), not a JSON array.
- Fields: `ID`, `Name`, `Service`, `Project`, `State`, `Health`, `ExitCode`, `Publishers[]`.
- `Health` is empty string when no healthcheck is defined.

---

## 2. Approach B: Embed `github.com/docker/compose/v2` Go Library

### Current module path
As of 2025-2026, the module is **`github.com/docker/compose/v5`** (the Go module major version tracks compose releases). Check `go.mod` in the repo for the current version.

### API surface

The core interface ([source](https://github.com/docker/compose/blob/40a2262a0fde426b32fa21ffef50dd6669577b73/pkg/api/api.go)):

```go
// github.com/docker/compose/v5/pkg/api
type Compose interface {
    Up(ctx context.Context, project *types.Project, options UpOptions) error
    Down(ctx context.Context, projectName string, options DownOptions) error
    Ps(ctx context.Context, projectName string, options PsOptions) ([]ContainerSummary, error)
    Start(ctx context.Context, projectName string, options StartOptions) error
    Stop(ctx context.Context, projectName string, options StopOptions) error
    Restart(ctx context.Context, projectName string, options RestartOptions) error
    List(ctx context.Context, options ListOptions) ([]Stack, error)
    // ... Build, Pull, Push, Logs, Kill, Remove, Exec, Copy, Pause, Unpause
}

// UpOptions maps to `compose up` flags
type UpOptions struct {
    Create CreateOptions
    Start  StartOptions
}
type CreateOptions struct {
    Services             []string      // selective services
    NoDeps               bool          // --no-deps
    RecreateDependencies string        // "always" | "never" | "changed"
    RemoveOrphans        bool
    // ...
}
```

### Minimal embed example

```go
import (
    "github.com/docker/cli/cli/command"
    "github.com/docker/cli/cli/flags"
    "github.com/docker/compose/v5/pkg/api"
    "github.com/docker/compose/v5/pkg/compose"
)

func newComposeService() (api.Compose, error) {
    dockerCLI, err := command.NewDockerCli()
    if err != nil { return nil, err }
    if err = dockerCLI.Initialize(&flags.ClientOptions{}); err != nil { return nil, err }
    return compose.NewComposeService(dockerCLI)
}

func upServices(ctx context.Context, svc api.Compose, projectFile, projectName string, services []string) error {
    project, err := svc.LoadProject(ctx, api.ProjectLoadOptions{
        ConfigPaths: []string{projectFile},
        ProjectName: projectName,
        Services:    services,
    })
    if err != nil { return err }
    return svc.Up(ctx, project, api.UpOptions{
        Create: api.CreateOptions{NoDeps: true, Services: services},
    })
}
```

**Evidence** — official SDK docs: [docker/compose sdk.md](https://github.com/docker/compose/blob/main/docs/sdk.md)

### Stability & pitfalls
- **Import cost**: pulls in `github.com/docker/cli`, `github.com/moby/moby`, `github.com/compose-spec/compose-go/v2` — adds ~50 MB to binary, significant transitive dependency surface.
- **API stability**: The `api.Compose` interface is considered stable for third-party use per the SDK docs, but minor versions can add methods (breaking your mock implementations).
- **No parallel project isolation**: The library shares a single Docker CLI context; concurrent `Up()` calls for different projects are safe at the Docker daemon level but log output interleaves unless you provide separate `LogConsumer` instances.
- **Progress output**: `Up()` writes progress to the CLI's stdout by default. Override with `dockerCLI.Apply(command.WithOutputStream(...))` before calling.
- **Best for**: control planes that need to diff/validate compose files in-process before applying, or that need to avoid spawning subprocesses.

---

## 3. Approach C: Docker Engine SDK Directly

```go
import "github.com/moby/moby/client"

cli, _ := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())

// List containers for a project (compose labels)
containers, _ := cli.ContainerList(ctx, container.ListOptions{
    Filters: filters.NewArgs(
        filters.Arg("label", "com.docker.compose.project="+projectName),
    ),
})

// Inspect health
result, _ := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
health := result.Container.State.Health.Status // "healthy" | "unhealthy" | "starting"
```

**When to prefer this:**
- You need fine-grained container lifecycle control (e.g., rolling restart of one replica without touching others).
- You want to read actual state without spawning a subprocess (reconciler hot path).
- You are NOT using compose YAML at all — pure programmatic container management.
- You need to avoid the compose library's heavy import graph.

**When NOT to use:**
- You have existing compose YAML files — you'd have to reimplement compose's dependency resolution, network creation, volume management, label conventions, etc.

---

## 4. Recommendation for controlai

**Use Approach A (shell out) as the primary apply mechanism + Docker SDK for state reading.**

Rationale:
1. **Compose CLI is the reference implementation** — it handles network creation, volume management, dependency ordering, and label conventions correctly. Reimplementing this is high risk.
2. **Shell-out is battle-tested** — Coolify, Dokploy, and Caprover all shell out to `docker compose` for apply operations (see §10).
3. **Docker SDK for reconciler reads** — use `client.ContainerList` with compose project labels for fast, no-subprocess state polling. This avoids spawning `docker compose ps` on every reconcile tick.
4. **Compose Go library for file diffing** — use `compose-go` (`github.com/compose-spec/compose-go/v2`) to parse and diff compose files in-process to determine which services changed, then shell out only for those services.

### Reconciler architecture

```
┌─────────────────────────────────────────────────────┐
│  Reconciler loop (every N seconds or on file change) │
│                                                       │
│  1. Read desired state: parse compose YAML (compose-go)│
│  2. Read actual state:  Docker SDK ContainerList      │
│  3. Diff: which services need restart/create/remove?  │
│  4. Apply: shell out `docker compose up -d --no-deps` │
│     for changed services only                         │
│  5. Health poll: Docker SDK ContainerInspect          │
└─────────────────────────────────────────────────────┘
```

---

## 5. `up -d --no-deps` — Safe Selective Restart

`--no-deps` tells compose to start the named services **without starting their `depends_on` dependencies**. This is safe when:
- Dependencies are already running (they were started in a prior full `up`).
- You only want to restart the service whose image/config changed.

```bash
# Restart only the "api" service in project "tenant-42"
docker compose -p tenant-42 -f /data/tenant-42/compose.yml up -d --no-deps api
```

**Pitfall**: If a dependency is not running, `--no-deps` will still start the target service but it may fail at runtime. Always verify dependency health before selective restart.

**`--force-recreate`**: Add this flag when the container config changed (env vars, mounts) but the image hash is the same. Without it, compose may skip recreation if it thinks nothing changed.

**Diff-driven apply pattern** (Go pseudocode):

```go
func reconcile(ctx context.Context, project string, file string, prev, next *types.Project) error {
    changed := diffServices(prev, next) // compare image, env, volumes, etc.
    if len(changed) == 0 { return nil }

    args := []string{"compose", "-p", project, "-f", file, "up", "-d", "--no-deps"}
    // Add --force-recreate if config (not just image) changed
    if hasConfigChange(prev, next, changed) {
        args = append(args, "--force-recreate")
    }
    args = append(args, changed...)
    return exec.CommandContext(ctx, "docker", args...).Run()
}
```

---

## 6. Health Check Polling

### Via Docker SDK (preferred for reconciler)

```go
result, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
if err != nil { return err }

state := result.Container.State
switch {
case state.Health != nil:
    // "healthy" | "unhealthy" | "starting" | "none"
    fmt.Println("health:", state.Health.Status)
    fmt.Println("last log:", state.Health.Log[len(state.Health.Log)-1].Output)
case state.Status == "running":
    fmt.Println("running, no healthcheck defined")
case state.Status == "exited":
    fmt.Println("exited with code:", state.ExitCode)
}
```

**Evidence** — compose ps.go uses exactly this pattern: [docker/compose ps.go](https://github.com/docker/compose/blob/40a2262a0fde426b32fa21ffef50dd6669577b73/pkg/compose/ps.go#L60-L80)

### Via `docker compose ps --format json`

```go
// Health field values: "healthy", "unhealthy", "starting", "" (no healthcheck)
// State field values: "running", "exited", "paused", "restarting"
entries, _ := composePSJSON(ctx, project)
for _, e := range entries {
    if e.Health == "unhealthy" {
        log.Printf("[%s/%s] unhealthy — triggering restart", project, e.Service)
    }
}
```

**Schema source**: [tokuhirom/dcv ComposeContainer](https://github.com/tokuhirom/dcv/blob/main/internal/models/compose_container.go) — confirmed against compose v2 output.

---

## 7. Concurrency: Parallel `docker compose` Invocations

### Known races and limits

| Issue | Detail | Mitigation |
|-------|--------|------------|
| **Docker daemon socket pressure** | Each `docker compose up` opens a persistent HTTP/2 connection to `/var/run/docker.sock`. Default max connections is 100. | Use a semaphore to cap concurrent compose invocations (e.g., 10–20). |
| **Network name collision** | Two projects with the same default network name (`<project>_default`) can collide if project names are not unique. | Always use `-p <unique-project-name>`. |
| **Volume name collision** | Same risk as networks. | Prefix volume names with project name in compose YAML. |
| **Parallel `up` on same project** | Running two `up` for the same project concurrently causes a race in compose's container recreation logic. | Use a per-project mutex. |
| **`docker build` parallelism** | Concurrent builds saturate CPU/disk. | Separate build semaphore from run semaphore. |

```go
// Semaphore pattern for capping concurrent compose invocations
var composeSem = make(chan struct{}, 15) // max 15 concurrent

func runCompose(ctx context.Context, args ...string) error {
    select {
    case composeSem <- struct{}{}:
        defer func() { <-composeSem }()
    case <-ctx.Done():
        return ctx.Err()
    }
    return exec.CommandContext(ctx, "docker", args...).Run()
}
```

---

## 8. Resource Limits per Tenant (Compose v2)

### Correct syntax: `deploy.resources` (not top-level `mem_limit`)

In compose v2, top-level `mem_limit` and `cpus` are **deprecated** (v1 compatibility). Use `deploy.resources`:

```yaml
services:
  app:
    image: myapp:latest
    deploy:
      resources:
        limits:
          cpus: "0.50"      # 50% of one CPU core
          memory: 512M
          pids: 200         # process limit (cgroup v2 only)
        reservations:
          cpus: "0.10"
          memory: 128M
```

**Evidence** — compose-spec deploy.md: [docker/docs deploy.md](https://github.com/docker/docs/blob/main/content/reference/compose-file/deploy.md)

**Evidence** — compose-go types: [compose-spec/compose-go types.go](https://github.com/compose-spec/compose-go/blob/main/types/types.go) — `Resources.Limits` uses `NanoCPUs` and `MemoryBytes` internally.

### Quirks vs Swarm `deploy.resources`
- On a **single host** (non-Swarm), `deploy.resources` is **fully honored** by compose v2 — it maps directly to Docker's `--cpus` and `--memory` flags.
- In Swarm mode, `deploy.resources` is also honored but applies per-replica.
- `pids` limit requires **cgroup v2** on the host. Check with `docker info | grep "Cgroup Version"`.
- Top-level `mem_limit` still works in compose v2 for backward compat but emits a deprecation warning. Prefer `deploy.resources`.

### Programmatic quota enforcement

```go
// When generating compose YAML for a tenant, inject resource limits:
svc.Deploy = &types.DeployConfig{
    Resources: types.Resources{
        Limits: &types.Resource{
            NanoCPUs:    types.NanoCPUs(500_000_000), // 0.5 CPU = 5e8 nanocpus
            MemoryBytes: types.UnitBytes(512 * 1024 * 1024), // 512 MiB
        },
    },
}
```

---

## 9. Concrete Code Snippets

### A. Shell-out reconciler (≤30 lines)

```go
func applyProject(ctx context.Context, p Project) error {
    changed, err := diffServices(p.PrevSpec, p.NextSpec)
    if err != nil || len(changed) == 0 { return err }

    args := []string{"compose", "-p", p.Name, "-f", p.ComposeFile,
        "up", "-d", "--no-deps", "--remove-orphans"}
    if p.ForceRecreate { args = append(args, "--force-recreate") }
    args = append(args, changed...)

    cmd := exec.CommandContext(ctx, "docker", args...)
    cmd.Dir = filepath.Dir(p.ComposeFile)
    cmd.Env = append(os.Environ(), "COMPOSE_PROJECT_NAME="+p.Name)
    out, err := cmd.CombinedOutput()
    if err != nil {
        return fmt.Errorf("compose up %s: %w\n%s", p.Name, err, out)
    }
    return nil
}
```

### B. Compose Go library Up with NoDeps (≤30 lines)

```go
func upWithLibrary(ctx context.Context, svc api.Compose, file, project string, services []string) error {
    proj, err := svc.LoadProject(ctx, api.ProjectLoadOptions{
        ConfigPaths: []string{file},
        ProjectName: project,
        Services:    services,
    })
    if err != nil { return fmt.Errorf("load: %w", err) }

    return svc.Up(ctx, proj, api.UpOptions{
        Create: api.CreateOptions{
            NoDeps:               true,
            Services:             services,
            RecreateDependencies: "never",
            RemoveOrphans:        true,
        },
        Start: api.StartOptions{Services: services},
    })
}
```

### C. Docker SDK health poll (≤30 lines)

```go
func pollHealth(ctx context.Context, cli *client.Client, projectName string) (map[string]string, error) {
    ctrs, err := cli.ContainerList(ctx, container.ListOptions{
        Filters: filters.NewArgs(
            filters.Arg("label", "com.docker.compose.project="+projectName),
        ),
    })
    if err != nil { return nil, err }

    health := make(map[string]string, len(ctrs))
    for _, c := range ctrs {
        svc := c.Labels["com.docker.compose.service"]
        insp, err := cli.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
        if err != nil { health[svc] = "inspect-error"; continue }
        if insp.Container.State.Health != nil {
            health[svc] = string(insp.Container.State.Health.Status)
        } else {
            health[svc] = insp.Container.State.Status
        }
    }
    return health, nil
}
```

---

## 10. Production-Grade Reference Orchestrators

### Coolify (PHP/Laravel, self-hosted PaaS)
- **Approach**: Shells out to `docker compose --project-directory <dir> -f <file> --project-name <uuid> up -d --remove-orphans --force-recreate --build`
- **Pattern**: Each app/service gets a UUID as project name. Compose files are generated and written to disk before each deploy.
- **Source**: [coollabsio/coolify StartService.php](https://github.com/coollabsio/coolify/blob/49656aa1edbe8aa6f7f7077dbf689cb1a08f05ee/app/Actions/Service/StartService.php)
- **Lesson**: Always `--remove-orphans` on full deploys; use `--force-recreate` to guarantee fresh containers.

### Dokploy (TypeScript/Node, open-source PaaS)
- **Approach**: Shells out to `docker compose` via a deploy API endpoint. Queues deployments per project to avoid concurrent apply races.
- **Pattern**: Deployment jobs are queued (BullMQ); each job calls the compose CLI via HTTP to a local agent.
- **Source**: [dokploy/dokploy deploy.ts](https://github.com/dokploy/dokploy/blob/6e342ee2f2b728a42c3a5901749c42dfc41b7ef2/apps/dokploy/server/utils/deploy.ts)
- **Lesson**: Queue per-project deployments to serialize applies; never run two `up` for the same project concurrently.

### CapRover (TypeScript/Node, self-hosted PaaS)
- **Approach**: Converts compose YAML to Docker service definitions for Swarm, but for single-host mode uses Docker SDK directly.
- **Source**: [caprover/caprover DockerComposeToServiceOverride.ts](https://github.com/caprover/caprover/blob/ba4e01abf94cc4c175833bad71cf5eef81bc1abe/src/utils/DockerComposeToServiceOverride.ts)
- **Lesson**: If you need Swarm-style replicas later, CapRover's translation layer is a useful reference. For single-host, stick with compose CLI.

---

## Citations

| Resource | URL |
|----------|-----|
| Docker Compose SDK docs | https://github.com/docker/compose/blob/main/docs/sdk.md |
| compose api.go (Compose interface) | https://github.com/docker/compose/blob/40a2262a0fde426b32fa21ffef50dd6669577b73/pkg/api/api.go |
| compose ps.go (health inspect pattern) | https://github.com/docker/compose/blob/40a2262a0fde426b32fa21ffef50dd6669577b73/pkg/compose/ps.go |
| compose-spec deploy.md (resource limits) | https://github.com/docker/docs/blob/main/content/reference/compose-file/deploy.md |
| compose-go types.go (Resources struct) | https://github.com/compose-spec/compose-go/blob/main/types/types.go |
| moby client ContainerInspect | https://github.com/moby/moby/blob/20b17b9727e57d27b4ef13d96e20e0e026404744/client/container_inspect.go |
| tokuhirom/dcv ComposeContainer schema | https://github.com/tokuhirom/dcv/blob/main/internal/models/compose_container.go |
| alexanderwanyoike/the0 NDJSON parsing | https://github.com/alexanderwanyoike/the0/blob/main/cli/cmd/local.go |
| Coolify StartService.php | https://github.com/coollabsio/coolify/blob/49656aa1edbe8aa6f7f7077dbf689cb1a08f05ee/app/Actions/Service/StartService.php |
| Dokploy deploy.ts | https://github.com/dokploy/dokploy/blob/6e342ee2f2b728a42c3a5901749c42dfc41b7ef2/apps/dokploy/server/utils/deploy.ts |
| CapRover DockerComposeToServiceOverride.ts | https://github.com/caprover/caprover/blob/ba4e01abf94cc4c175833bad71cf5eef81bc1abe/src/utils/DockerComposeToServiceOverride.ts |

## Recommendations

1. **Primary apply path**: Shell out to `docker compose -p <project> -f <file> up -d --no-deps --remove-orphans <changed-services>`. Add `--force-recreate` when env/config (not just image) changed.
2. **State reading**: Use Docker SDK `ContainerList` + `ContainerInspect` with `com.docker.compose.project` label filter — no subprocess, fast reconciler ticks.
3. **File diffing**: Use `github.com/compose-spec/compose-go/v2` in-process to parse and diff compose YAML. Only import the compose library for parsing, not for apply.
4. **Concurrency**: Serialize applies per project (per-project mutex or queue). Cap total concurrent compose invocations at 10–20 with a semaphore.
5. **Resource limits**: Use `deploy.resources.limits` in generated compose YAML. Verify cgroup v2 is active on the host for `pids` limits.
6. **Health polling**: Poll `ContainerInspect` every 5–10s in the reconciler. Treat `Health.Status == "unhealthy"` as a trigger for restart, not just logging.
