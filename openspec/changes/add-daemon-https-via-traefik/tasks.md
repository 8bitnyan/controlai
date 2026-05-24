# Tasks: add-daemon-https-via-traefik

## 1. Traefik dynamic configuration

- [ ] 1.1 Create `deploy/aws/traefik/dynamic/api.yml.tmpl` Go template that renders a Traefik router rule matching `Host("api.${DEPLOYMENT_NAME}.sslip.io")` on entrypoint `websecure` with TLS cert resolver `letsencrypt` proxying to `http://127.0.0.1:${DAEMON_TCP_PORT}`.
- [ ] 1.2 Add a Traefik middleware `add-bearer-passthrough` (no-op — bearer token is validated by daemon, not Traefik) to the router so middleware slot is explicit and extensible.
- [ ] 1.3 Verify Traefik static config (`deploy/aws/traefik/traefik.yml`) already declares `websecure` entrypoint on :443 and ACME cert resolver; add if missing.
- [ ] 1.4 Confirm Traefik static config has `api.insecure: false` and dashboard disabled on public entrypoints.
- [ ] 1.5 Add a `certificatesResolvers.letsencrypt` block with `acme.httpChallenge.entryPoint: web` to the static config if not already present.
- [ ] 1.6 Add `ACME_EMAIL` and `ACME_STAGING` variables to `deploy/aws/env.sh.example`; default `ACME_STAGING=1` for first-time provisioners.
- [ ] 1.7 Render `api.yml` from template in `up.sh` (replace `${DEPLOYMENT_NAME}` and `${DAEMON_TCP_PORT}` using `envsubst`), write to `/etc/traefik/dynamic/api.yml` on the EC2 host.

## 2. AWS security group validation

- [ ] 2.1 Add an assertion in `up.sh` (before printing the summary block) that port 443 is open in the security group; if missing, add an ingress rule via `aws ec2 authorize-security-group-ingress`.
- [ ] 2.2 Add an assertion that port 80 is open (required for ACME HTTP-01 challenge); same remediation pattern.
- [ ] 2.3 Document in `deploy/aws/README.md` that both ports 80 and 443 must be open for HTTPS API to function.

## 3. Let's Encrypt staging → production transition

- [ ] 3.1 When `ACME_STAGING=0`, set `acme.caServer` to the production LE endpoint in the rendered static config.
- [ ] 3.2 When `ACME_STAGING=1` (default), use `https://acme-staging-v02.api.letsencrypt.org/directory` and print a warning banner in `up.sh` output.
- [ ] 3.3 Add a `deploy/aws/scripts/flip-acme-prod.sh` helper that SSH-es to the instance and flips `ACME_STAGING` to 0 + restarts Traefik.

## 4. Bearer token end-to-end validation

- [ ] 4.1 In `up.sh` post-provision smoke-test: create a test token via `ssh controlai@$HOST "controlai token create smoke-test"`, capture the token, call `https://api.$DEPLOYMENT_NAME.sslip.io/v1/health` with `curl -sf --max-time 30 -H "Authorization: Bearer $TOKEN"`, assert HTTP 200.
- [ ] 4.2 Revoke the smoke-test token after validation: `ssh controlai@$HOST "controlai token revoke smoke-test"`.
- [ ] 4.3 Add a check that a request without a bearer token returns HTTP 401.
- [ ] 4.4 Document `controlai token create web-bff` in `deploy/aws/README.md` as the recommended step for provisioning a long-lived token for the controlai-web BFF.

## 5. Integration test

- [ ] 5.1 Create `deploy/aws/test/test_https_api.sh` that asserts: HTTPS endpoint reachable, cert valid, `/v1/health` returns `{"status":"healthy"}` with bearer token, returns 401 without token.
- [ ] 5.2 Add `make test-aws-https` target in root `Makefile` (or equivalent) that sources `deploy/aws/.state/$DEPLOYMENT_NAME.json` and runs the test script.
- [ ] 5.3 Add CI job `test-aws-https` (runs only when `deploy/aws/**` changes and `INTEGRATION=1` is set) that provisions an ephemeral deployment, runs the test, then tears down.

## 6. `up.sh` summary block update

- [ ] 6.1 Add `API URL: https://api.$DEPLOYMENT_NAME.sslip.io` line to the summary block printed by `up.sh`.
- [ ] 6.2 Add `Next step: create a bearer token with: controlai token create web-bff` hint to the summary block.
- [ ] 6.3 Poll `https://api.$DEPLOYMENT_NAME.sslip.io/v1/health` with a 60-second timeout in `up.sh`; print "API endpoint healthy" or "cert still pending — check Traefik logs" accordingly.

## 7. Acceptance

- [ ] 7.1 Run `openspec validate add-daemon-https-via-traefik --strict` and confirm exit 0.
- [ ] 7.2 Verify `deploy/aws/up.sh` prints the new API URL in its summary block in a dry-run or integration test.
- [ ] 7.3 Verify a `curl` call with bearer token to the HTTPS API returns HTTP 200 on a live deployment.
- [ ] 7.4 Verify a `curl` call without bearer token returns HTTP 401.
- [ ] 7.5 Verify `down.sh` removes no new resources introduced by this change (Traefik config is on-host, not an AWS resource).
