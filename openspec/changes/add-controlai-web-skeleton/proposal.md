# Change: Bootstrap controlai-web — monorepo skeleton, auth, domain, and instance registry

## Why

The user asked for a modular web GUI for controlai: "the goal is to create an account
and project and thus site based sensor-gateway-broker-ingest-timescale configuration
provisioning and monitoring using a node editor." The existing controlai daemon has no
web frontend and is operated entirely via CLI. A greenfield sibling repo
`8bitnyan/controlai-web` (TypeScript, Next.js 16, tRPC, Prisma, better-auth) will
provide a SaaS-mode federation control plane that calls one or many controlai daemons
over HTTPS. This change establishes the repo skeleton, the multi-tenant data model,
authentication, and the instance registry — the foundation that `add-controlai-web-pipeline-ux`
builds the node editor and monitoring on top of.

## What Changes

- **NEW REPO** `8bitnyan/controlai-web` (public, MIT) — created as a pnpm monorepo with
  Turborepo.
- **4 new capability specs** added to `openspec/changes/add-controlai-web-skeleton/specs/`:
  - `controlai-web-shell` — monorepo layout, build system, env var contract, CI workflow
  - `controlai-web-auth` — better-auth v1.6+ email+password, organization plugin, sessions, roles
  - `controlai-web-domain` — Org/Project/SiteGroup/Site Prisma models, ABAC middleware, audit log
  - `controlai-web-instance-registry` — ControlaiInstance model, encryption, health polling, token rotation
- No existing controlai specs are modified; all 4 capability specs are NET-NEW (controlai-web does not yet exist).

## Impact

- Affected specs: none existing (all 4 are ADDED as new capabilities)
- Affected code: new sibling repository `controlai-web/` — not pre-created in this run;
  actual scaffolding is a task in `tasks.md`, executed in a future coding session
- Depends on: `add-daemon-https-via-traefik` (daemon must have HTTPS before BFF can register an instance)
- Breaking changes: **none** — additive new repo; existing controlai daemon and its specs untouched
- Reference codebases used for architecture decisions: sdi_oc (~80%), modules_cloud-main (~15%)
