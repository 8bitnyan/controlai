# Change: Expose controlai daemon REST over public HTTPS via Traefik

## Why

`controlai-web` (the upcoming web control plane — see `add-controlai-web-skeleton`) must
call the controlai daemon REST API from a Vercel-hosted BFF server. Today the daemon is
reachable only via unix socket (CLI) or a plain HTTP TCP listener with no public route.
The web BFF needs a stable, TLS-secured, bearer-token-authenticated HTTPS endpoint so it
can call the daemon from outside the EC2 host without shipping SSH-tunnel plumbing.
Traefik is already running on the host (from `add-aws-provisioning`) and already handles
HTTP/443 for the sslip.io domain — adding a sub-domain route costs one Traefik config
file with zero daemon Go code changes. (Decision D14 from the controlai-web interview.)

## What Changes

- **Traefik dynamic configuration** gains a new router `api-<deployment>` that matches
  `Host("api.<DEPLOYMENT_NAME>.sslip.io")` and proxies to the controlai daemon TCP listener.
- **TLS** is provided by Traefik's built-in ACME HTTP-01 challenge (Let's Encrypt) using
  the existing port-80 entrypoint already open on the EC2 security group.
- **`deploy/aws/up.sh`** documents the new API subdomain in its post-provision summary and
  verifies DNS + cert issuance before printing "ready".
- **`deploy/aws/security_group.tf` (or equivalent shell AWS CLI call)** confirms port 443
  is open — it already is per `add-aws-provisioning`, but this change adds an explicit
  smoke-test step.
- **Bearer-token auth** is already implemented in the daemon (`:1229-1247` in
  `internal/daemon/server.go`); this change validates the end-to-end path and adds an
  integration test.
- Spec `aws-provisioning` is **MODIFIED** to add the HTTPS API route requirement.

## Impact

- Affected specs: `aws-provisioning` (modified — additive only, no breaking change)
- Affected code: `deploy/aws/` Traefik config files; `deploy/aws/up.sh` summary block;
  integration test `deploy/aws/test_https.sh`
- No daemon Go code change required
- No existing tenant/site routing affected (Traefik routes are additive)
- Breaking changes: **none** — existing unix-socket and plain-TCP paths continue to work
