## ADDED Requirements

### Requirement: pnpm monorepo with Turborepo build pipeline

`controlai-web` SHALL be structured as a pnpm workspace monorepo under
`8bitnyan/controlai-web` (public, MIT) with the following top-level layout:
`apps/web` (Next.js 16 App Router), `packages/api` (tRPC server + routers),
`packages/db` (Prisma client + schema), `packages/shared-types` (enums + Zod schemas).
Turborepo SHALL orchestrate the `build`, `lint`, `typecheck`, and `test` pipeline tasks
across all packages. TypeScript strict mode SHALL be enforced via a shared
`tsconfig.base.json`.

#### Scenario: Root build completes cleanly

- **WHEN** a developer runs `pnpm turbo run build` from the repo root
- **THEN** all packages and apps SHALL compile without TypeScript errors
- **AND** the output SHALL include `apps/web/.next/` and `packages/*/dist/` artifacts

#### Scenario: Lint and typecheck pass on clean checkout

- **WHEN** a developer runs `pnpm turbo run lint typecheck` from the repo root on a fresh clone
- **THEN** ESLint and TypeScript type-checking SHALL complete with zero errors or warnings
- **AND** the run SHALL finish in under 60 s on a standard CI runner

#### Scenario: Package boundary enforcement

- **WHEN** code in `apps/web` imports from `packages/api` or `packages/db`
- **THEN** the import SHALL resolve via the declared workspace dependency (`@controlai-web/api`, `@controlai-web/db`)
- **AND** direct cross-package file path imports (e.g. `../../packages/api/src/...`) SHALL be absent

### Requirement: Environment variable contract

`controlai-web` SHALL declare all required and optional environment variables in
`apps/web/.env.example` with documentation. The application SHALL fail fast at startup
if any required variable is missing, printing a clear error message naming the missing
variable. Optional variables SHALL have documented defaults or marked as `# Phase 2`.

#### Scenario: Missing required env var at startup

- **WHEN** `apps/web` starts without `BETTER_AUTH_SECRET` set
- **THEN** the process SHALL exit with a non-zero code before accepting any HTTP requests
- **AND** the error message SHALL name the missing variable and link to the docs

#### Scenario: .env.example is complete

- **WHEN** a new developer copies `.env.example` to `.env.local` and fills in the
  required values
- **THEN** `pnpm dev` SHALL start without env-related errors
- **AND** all Phase-2 variables marked as optional SHALL be safely absent

### Requirement: CI workflow — lint, typecheck, unit test, E2E

`controlai-web` SHALL include a GitHub Actions workflow (`.github/workflows/ci.yml`)
that runs on every push to `main` and every pull request. The workflow SHALL run:
`lint`, `typecheck`, `unit-test` (Vitest), and on PRs to `main` also `e2e` (Playwright
Chromium-only). All jobs SHALL pass before a PR may be merged.

#### Scenario: PR CI passes on a clean feature branch

- **WHEN** a developer opens a pull request to `main` with no TypeScript errors, no ESLint violations, all Vitest tests green, and all Playwright E2E happy-path tests green
- **THEN** the CI workflow SHALL complete with a green checkmark on all jobs
- **AND** the PR merge button SHALL be unblocked (branch protection enforces CI)

#### Scenario: CI blocks on lint failure

- **WHEN** a PR introduces an ESLint violation (e.g. `no-unused-vars`)
- **THEN** the `lint` job SHALL fail with a non-zero exit code
- **AND** the PR merge button SHALL remain blocked until the violation is resolved

### Requirement: Prettier formatting enforced

All TypeScript, TSX, JSON, and Markdown files in the monorepo SHALL conform to the
Prettier configuration in `.prettierrc`. A `format:check` script SHALL be included in
the root `package.json` and run in CI. Developers SHALL use `pnpm format` to auto-fix.

#### Scenario: Format check catches unformatted file

- **WHEN** a developer commits a file with trailing spaces or mismatched quote style
- **THEN** `pnpm turbo run format:check` SHALL exit non-zero
- **AND** the CI `lint` job SHALL fail with a message naming the unformatted file

#### Scenario: Format auto-fix resolves violations

- **WHEN** a developer runs `pnpm format` from the repo root
- **THEN** Prettier SHALL rewrite all non-conforming files in-place
- **AND** `pnpm turbo run format:check` SHALL subsequently exit 0
