# aws-provisioning Specification

## Purpose
TBD - created by archiving change add-daemon-https-via-traefik. Update Purpose after archive.
## Requirements
### Requirement: Public HTTPS API endpoint via Traefik

When a controlai deployment is provisioned via `deploy/aws/up.sh`, Traefik SHALL
expose the controlai daemon REST API at `https://api.<DEPLOYMENT_NAME>.sslip.io/`
secured with a Let's Encrypt TLS certificate and protected by the daemon's existing
bearer-token authentication. No daemon Go code change is required; Traefik terminates
TLS and proxies to the daemon's TCP listener on localhost.

#### Scenario: HTTPS endpoint available after provisioning

- **WHEN** `deploy/aws/up.sh` completes successfully for a new deployment
- **THEN** `https://api.<DEPLOYMENT_NAME>.sslip.io/v1/health` SHALL return HTTP 200 with
  a valid Let's Encrypt TLS certificate when called with a valid `Authorization: Bearer <token>` header
- **AND** the `up.sh` summary block SHALL print the API URL and a hint to create a bearer token

#### Scenario: Missing bearer token rejected

- **WHEN** a client calls `https://api.<DEPLOYMENT_NAME>.sslip.io/v1/health` without an
  `Authorization` header
- **THEN** the daemon SHALL respond with HTTP 401 and a JSON error body
- **AND** no sensitive daemon state SHALL be exposed in the response

#### Scenario: ACME HTTP-01 cert acquisition

- **WHEN** Traefik starts on a freshly provisioned host with `certificatesResolvers.letsencrypt`
  configured and port 80 open in the security group
- **THEN** Traefik SHALL automatically acquire a Let's Encrypt certificate for
  `api.<DEPLOYMENT_NAME>.sslip.io` within 60 seconds of start
- **AND** the certificate SHALL auto-renew before expiry with no operator intervention

#### Scenario: Staging issuer during development

- **WHEN** `ACME_STAGING=1` (the default) is set before running `up.sh`
- **THEN** Traefik SHALL use the Let's Encrypt staging CA
- **AND** `up.sh` SHALL print a warning banner noting the cert is not browser-trusted

#### Scenario: Production issuer after validation

- **WHEN** `ACME_STAGING=0` is set and `deploy/aws/scripts/flip-acme-prod.sh` is run
- **THEN** Traefik SHALL switch to the Let's Encrypt production CA on next renewal
- **AND** the existing staging cert SHALL be replaced within 90 days (or immediately if forced)

### Requirement: Bearer token provisioning for web BFF

The `deploy/aws/up.sh` summary block SHALL guide the operator to create a named
bearer token using `controlai token create web-bff` immediately after provisioning, so
that the token can be pasted into the controlai-web BFF environment configuration.
The smoke-test SHALL create and then revoke a transient token to validate the
end-to-end bearer-token path.

#### Scenario: Smoke-test token lifecycle

- **WHEN** `up.sh` runs its post-provision smoke-test
- **THEN** it SHALL SSH to the host, create token `smoke-test`, call the HTTPS API with
  that token to assert HTTP 200, then revoke the `smoke-test` token
- **AND** the final output SHALL include `API endpoint: HEALTHY` or a clear failure message

#### Scenario: Operator creates long-lived web-bff token

- **WHEN** the operator runs `controlai token create web-bff` on the EC2 host
- **THEN** the daemon SHALL return a plaintext bearer token exactly once
- **AND** the `up.sh` summary block SHALL remind the operator to save it securely and
  configure it as `CONTROLAI_BEARER_TOKEN` in the controlai-web Vercel environment

