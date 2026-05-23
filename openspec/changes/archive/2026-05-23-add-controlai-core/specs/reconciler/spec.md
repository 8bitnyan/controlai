## ADDED Requirements

### Requirement: Background reconciliation of desired vs actual state

controlai SHALL run a reconciler loop inside the daemon that, on a base
period of 30 seconds, reads desired state from SQLite, polls actual
state from the Docker engine via the Docker SDK with a
`com.docker.compose.project` label filter, and drives convergence by
shelling out to `docker compose -p <project> -f <file> up -d --no-deps
<services>` through `internal/runner`. The reconciler SHALL be the only
component in controlai that issues compose mutations.

#### Scenario: Missing container is recreated within one period
- **WHEN** an operator manually runs `docker compose -p <site> down` on a site that controlai has `desired=running`
- **THEN** within 30 s the reconciler SHALL detect the absence, run `up -d`, and the site SHALL be running again

#### Scenario: Stopped state is honored
- **WHEN** an operator runs `controlai site stop <tenant> <site>` and the desired-state row is set to `stopped`
- **THEN** the reconciler SHALL NOT recreate the containers and the audit log SHALL show no spurious `up` attempts

### Requirement: Per-project mutex and global concurrency cap

The reconciler and any other compose caller SHALL serialize calls to the
same project with a per-project `sync.Mutex`, and SHALL bound concurrent
compose invocations globally to a `semaphore.Weighted(15)` regardless of
project, to prevent Docker socket pressure.

#### Scenario: Two simultaneous applies on the same project serialize
- **WHEN** two `controlai apply` invocations target the same site at the same time
- **THEN** the second SHALL block until the first releases the project mutex; neither SHALL produce a partial or interleaved compose state

#### Scenario: Sixteenth concurrent compose call waits
- **WHEN** 16 distinct projects are reconciling simultaneously
- **THEN** at most 15 SHALL be running `docker compose` at any instant; the 16th SHALL wait on the semaphore

### Requirement: Exponential backoff on failures and audit-event emission

The reconciler SHALL retry failed compose mutations on the sequence
30 s → 1 m → 5 m → 30 m → 30 m (capped), resetting the backoff state
on the next successful tick. The reconciler SHALL emit an
`audit_event(kind=reconciler.{success|failure|backoff})` with stderr
captured for every attempt regardless of outcome.

#### Scenario: Failing compose retries with backoff
- **WHEN** a compose mutation fails with exit code != 0
- **THEN** the next retry SHALL be scheduled at +30 s, then +1 m, then +5 m, then +30 m; the audit log SHALL capture each attempt's stderr

#### Scenario: Success resets backoff
- **WHEN** a previously-failing project converges successfully
- **THEN** subsequent failures (if any) SHALL restart from the 30 s rung, not from where the previous backoff sequence ended

### Requirement: Blocking apply command

`controlai apply <selector>` SHALL be a blocking CLI command that writes
the desired-state delta, then polls SQLite (via the daemon REST API) for
convergence up to a configurable timeout (default 120 s). On timeout the
command SHALL exit non-zero with the last observed mismatch reason.

#### Scenario: Apply blocks until reconciled
- **WHEN** an operator changes a site's broker kind and runs `controlai site apply <tenant> <site>`
- **THEN** the command SHALL return zero only after the reconciler reports the site's actual state matches the new desired hash, within the timeout
