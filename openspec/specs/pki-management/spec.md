# pki-management Specification

## Purpose
TBD - created by archiving change add-controlai-core. Update Purpose after archive.
## Requirements
### Requirement: Per-site self-signed CA with encrypted key storage

controlai SHALL generate one self-signed RSA-2048 CA per site at site
creation. The CA private key SHALL be wrapped with AES-256-GCM using a
master key read from the `CONTROLAI_CA_KEY_ENCRYPTION_KEY` environment
variable. The encrypted blob SHALL be stored in the SQLite `cas` table.

#### Scenario: New site issues its own CA
- **WHEN** a site is created
- **THEN** controlai SHALL generate a new RSA-2048 CA with CN `controlai-<tenant_id>-<site_id>-ca`, default validity 20 years, and persist the AES-GCM-wrapped private key in SQLite

#### Scenario: Daemon refuses to start without master key in production
- **WHEN** the daemon is started in non-dev mode and `CONTROLAI_CA_KEY_ENCRYPTION_KEY` is unset
- **THEN** the daemon SHALL exit before opening listeners with a clear error message

### Requirement: Leaf certificate issuance for gateways

controlai SHALL issue RSA-2048 leaf certificates per gateway on demand,
signed by the parent site's CA. The leaf SHALL carry ClientAuth EKU
only, CN set to the slugified gateway name, default validity 365 days.
The private key SHALL be returned to the caller exactly once and SHALL
NOT be persisted server-side; only the public cert and metadata SHALL
be stored in the `certs` table.

#### Scenario: Issue cert for a gateway
- **WHEN** an operator runs `controlai pki cert issue --site tnt_acme-corp/ste_seoul --gateway floor-1-pump`
- **THEN** controlai SHALL emit a PEM-encoded cert and private key on stdout, persist only the cert and metadata, and the daemon SHALL never log the private key

### Requirement: Server certificate for site broker

controlai SHALL issue a server certificate for each site broker with
ServerAuth + ClientAuth EKU, SANs covering `ste_<id>.tnt_<id>.<base-domain>`
and any operator-provided aliases, default validity 10 years, written to
`/var/lib/controlai/tenants/<t>/sites/<s>/deploy/certs/active/`.

#### Scenario: Server cert reachable by broker container
- **WHEN** a site is created
- **THEN** the broker container SHALL mount `deploy/certs/active/server.crt` and `server.key`, and the broker SHALL start with TLS enabled on its internal :8883 listener

### Requirement: Automatic rotation triggers

controlai SHALL run `EnsureTrustFiles` at daemon start and on every
reconciler tick. Rotation SHALL be triggered when any of the following
hold for any cert: file missing on disk, fails to verify against the
active CA, expires within 30 days, OR (server only) the SAN set on disk
no longer matches the configured host list.

#### Scenario: Expiring cert is rotated automatically
- **WHEN** a leaf cert is within 30 days of expiry
- **THEN** controlai SHALL issue a replacement, write it to the active directory, restart the broker listener (EMQX REST API or container restart for mosquitto), and emit `audit_event(kind=pki.rotate)`

### Requirement: Revocation reflected in EMQX banned-list

controlai SHALL mark a revoked certificate revoked in SQLite and,
for EMQX-backed sites, push the entry into EMQX's banned client list
via `POST /api/v5/banned` keyed on `clientid=md5(cert DER hex)`. The
reconciler SHALL retry on failure with exponential backoff until success.

#### Scenario: Revocation propagates to EMQX
- **WHEN** an operator revokes a cert for an EMQX-backed site
- **THEN** within 60 s controlai SHALL have called the EMQX banned-list endpoint and the gateway SHALL be unable to reconnect with that cert

#### Scenario: Mosquitto revocation handled via CRL refresh
- **WHEN** an operator revokes a cert for a mosquitto-backed site
- **THEN** controlai SHALL regenerate the site's CRL file, restart the mosquitto container, and the gateway SHALL be unable to reconnect

