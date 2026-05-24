# Tasks: add-controlai-web-skeleton

## 1. Repo bootstrap

- [ ] 1.1 Run `gh repo create 8bitnyan/controlai-web --public --license=MIT --clone` to create and clone the new repo.
- [ ] 1.2 Create top-level `README.md` describing the project purpose, tech stack, and repo layout.
- [ ] 1.3 Create `.gitignore` (node_modules, .env.local, .turbo, .next, dist, *.tsbuildinfo).
- [ ] 1.4 Create `LICENSE` file with MIT license text and `Copyright (c) 2026 8bitnyan`.
- [ ] 1.5 Create `CONTRIBUTING.md` with branch naming (`feat/`, `fix/`, `chore/`) and conventional commit subject line requirements.
- [ ] 1.6 Push initial commit with README + LICENSE + .gitignore.

## 2. Monorepo & build tooling

- [ ] 2.1 Run `pnpm init` at repo root; set `"packageManager": "pnpm@9.x"` in `package.json`.
- [ ] 2.2 Create `pnpm-workspace.yaml` listing `apps/*` and `packages/*`.
- [ ] 2.3 Install Turborepo: `pnpm add -D turbo -w`; create `turbo.json` with `build`, `lint`, `typecheck`, `test` pipeline tasks.
- [ ] 2.4 Create `tsconfig.base.json` at repo root with `strict: true`, `moduleResolution: bundler`, `target: ES2022`, `lib: ["ES2022", "DOM"]`.
- [ ] 2.5 Install and configure Prettier: `.prettierrc` matching sdi_oc style (singleQuote, semi, 80 cols); `pnpm add -D prettier -w`.
- [ ] 2.6 Install and configure ESLint: `eslint.config.mjs` with `@typescript-eslint/recommended` + `eslint-plugin-react` + `eslint-config-next`; `pnpm add -D eslint typescript-eslint @typescript-eslint/eslint-plugin eslint-plugin-react eslint-config-next -w`.
- [ ] 2.7 Add `lint`, `typecheck`, `format:check` scripts to root `package.json` that delegate to `turbo run`.

## 3. Apps & packages scaffolding

- [ ] 3.1 Scaffold `apps/web` with `pnpm create next-app@latest apps/web --typescript --tailwind --app --no-src-dir --import-alias "@/*"`.
- [ ] 3.2 Create `packages/db/` with `package.json` (`name: @controlai-web/db`), `tsconfig.json` extending base, `src/index.ts` re-exporting Prisma client.
- [ ] 3.3 Create `packages/api/` with `package.json` (`name: @controlai-web/api`), `tsconfig.json`, `src/index.ts` re-exporting tRPC router and context.
- [ ] 3.4 Create `packages/shared-types/` with `package.json` (`name: @controlai-web/shared-types`), `tsconfig.json`, `src/index.ts` re-exporting domain enums and types (OrgRole, InstanceStatus, etc.).
- [ ] 3.5 Add workspace dependencies: `apps/web` depends on `@controlai-web/api` and `@controlai-web/db`; `packages/api` depends on `@controlai-web/db` and `@controlai-web/shared-types`.

## 4. Database — Prisma schema and migrations

- [ ] 4.1 Create a Neon Postgres project and note the connection string.
- [ ] 4.2 Install Prisma in `packages/db`: `pnpm add prisma @prisma/client @neondatabase/serverless -w --filter @controlai-web/db`.
- [ ] 4.3 Run `pnpm prisma init --datasource-provider postgresql` in `packages/db`; set `DATABASE_URL` to Neon connection string.
- [ ] 4.4 Write `packages/db/prisma/schema.prisma` with models: `User`, `Session`, `Account`, `Verification` (better-auth required), `Organization`, `OrganizationMember` (OrgRole enum: OWNER/ADMIN/MEMBER), `OrganizationInvitation`, `ControlaiInstance` (InstanceStatus enum: UNKNOWN/HEALTHY/DEGRADED/UNREACHABLE), `Project`, `SiteGroup`, `Site`, `AuditLog`, `Dashboard` (placeholder for pipeline-ux), `NodeConfig` (placeholder for pipeline-ux).
- [ ] 4.5 Run `pnpm prisma migrate dev --name init` to create the initial migration SQL.
- [ ] 4.6 Export Prisma client singleton in `packages/db/src/client.ts` using the `@neondatabase/serverless` adapter for Vercel edge compatibility.
- [ ] 4.7 Add `pnpm prisma generate` to the `db` workspace build script so client is always regenerated.
- [ ] 4.8 Write seed file `packages/db/prisma/seed.ts` that creates a dev user with `email: admin@localhost.dev` and a default org `dev-org`; run via `pnpm prisma db seed`.

## 5. Authentication — better-auth

- [ ] 5.1 Install better-auth in `packages/api`: `pnpm add better-auth @better-auth/prisma-adapter -w --filter @controlai-web/api`.
- [ ] 5.2 Create `packages/api/src/auth.ts` configuring better-auth with `prismaAdapter(prisma)`, `emailAndPassword({ enabled: true, requireEmailVerification: false })`, `organization({ allowUserToCreateOrganization: true, organizationLimit: 5 })`, and `secret: process.env.BETTER_AUTH_SECRET`.
- [ ] 5.3 Create `apps/web/app/api/auth/[...all]/route.ts` that imports the better-auth handler and exports GET + POST.
- [ ] 5.4 Create `apps/web/lib/auth-client.ts` with `createAuthClient({ baseURL: process.env.BETTER_AUTH_URL, plugins: [organizationClient()] })` for use in client components.
- [ ] 5.5 Create `apps/web/lib/auth-server.ts` re-exporting `auth.api` and a `getSession(req)` helper for use in tRPC context and Server Components.
- [ ] 5.6 Build sign-in page at `apps/web/app/(auth)/sign-in/page.tsx` with email+password form (shadcn `Input` + `Button`); submit via `authClient.signIn.email({email, password})`.
- [ ] 5.7 Build sign-up page at `apps/web/app/(auth)/sign-up/page.tsx` with name + email + password form; submit via `authClient.signUp.email({name, email, password})`.
- [ ] 5.8 Build user-menu component `apps/web/components/auth/user-menu.tsx` (avatar dropdown with sign-out button) modelled on sdi_oc's user-menu.

## 6. tRPC setup

- [ ] 6.1 Install tRPC: `pnpm add @trpc/server @trpc/client @trpc/react-query @trpc/next -w --filter @controlai-web/api`.
- [ ] 6.2 Create `packages/api/src/trpc.ts` with `initTRPC.context<TRPCContext>()` and procedures: `publicProcedure`, `protectedProcedure` (requires session), `orgProcedure` (requires orgId in input + verifies membership), `ownerAdminProcedure` (requires OWNER or ADMIN role).
- [ ] 6.3 Create tRPC context factory `packages/api/src/context.ts` that reads the session from `better-auth` cookie via `auth.api.getSession(req)`.
- [ ] 6.4 Create `packages/api/src/root.ts` with `appRouter` merging all sub-routers.
- [ ] 6.5 Create `apps/web/app/api/trpc/[trpc]/route.ts` using `fetchRequestHandler` adapter from `@trpc/server/adapters/fetch`.
- [ ] 6.6 Create `apps/web/lib/trpc/client.tsx` with `createTRPCReact<AppRouter>()` and `TRPCProvider` component wrapping `QueryClientProvider`.
- [ ] 6.7 Create `apps/web/lib/trpc/server.ts` with `createCaller` for Server Component tRPC calls.
- [ ] 6.8 Add ABAC error formatting: tRPC `onError` middleware logs `UNAUTHORIZED` and `FORBIDDEN` errors with `userId` + `orgId` for audit trail.

## 7. Domain routers

- [ ] 7.1 Create `packages/api/src/routers/org.ts` with procedures: `org.list`, `org.create` (slug unique-check), `org.update`, `org.delete` (OWNER only), `org.inviteMember`, `org.acceptInvitation`, `org.removeMember`, `org.updateMemberRole`.
- [ ] 7.2 Create `packages/api/src/routers/project.ts` with procedures: `project.list` (by orgId), `project.create` (requires instanceId in same org), `project.update`, `project.delete`; enforce OWNER/ADMIN only for create/update/delete.
- [ ] 7.3 Create `packages/api/src/routers/siteGroup.ts` with procedures: `siteGroup.list` (by projectId), `siteGroup.create`, `siteGroup.update`, `siteGroup.delete`.
- [ ] 7.4 Create `packages/api/src/routers/site.ts` with procedures: `site.list` (by siteGroupId), `site.create`, `site.update`, `site.delete`; `site.create` sets `controlaiTenantId` and `controlaiSiteId` to null (populated by Apply in pipeline-ux).
- [ ] 7.5 Create `packages/api/src/routers/audit.ts` with procedure `audit.list` (by orgId, with optional `action` and date-range filters); read-only in v1.
- [ ] 7.6 Create `packages/api/src/lib/audit-writer.ts` helper `writeAudit(db, { orgId, userId, action, targetId, targetType, metadata })` called from all mutating procedures.
- [ ] 7.7 Add Zod input schemas for all router procedures; export them from `packages/shared-types/src/validation.ts`.

## 8. Instance registry

- [ ] 8.1 Create `packages/api/src/lib/crypto.ts` with `encryptToken(plaintext: string): string` and `decryptToken(ciphertext: string): string` using Node.js `crypto` AES-256-GCM with `INSTANCE_TOKEN_KEY` env var; throw on missing key at startup.
- [ ] 8.2 Create `packages/api/src/routers/instance.ts` with procedures:
  - `instance.list` — list instances in org with status, lastSeenAt, capacityUsedMB/AllowedMB (token NOT returned)
  - `instance.register` — validate baseURL reachable + token valid by calling `GET /v1/health` with timeout 10 s, then insert with `encryptToken(token)`
  - `instance.testConnection` — decrypt token, call `GET /v1/health`, return status
  - `instance.update` — update name or token (re-encrypt new token)
  - `instance.delete` — OWNER only; block if projects still reference the instance
- [ ] 8.3 Create daemon client helper `packages/api/src/lib/daemon-client.ts` with `callDaemon<T>(instance: ControlaiInstance, path: string, options?: RequestInit): Promise<T>` that decrypts the bearer token and calls `fetch` with 30 s timeout and proper error wrapping.
- [ ] 8.4 Create Vercel Cron handler `apps/web/app/api/cron/instance-health/route.ts` (GET, secured by `CRON_SECRET` header check); calls `instance.healthPoll()` for all instances across all orgs; updates `status`, `lastSeenAt`, `version`, `capacityUsedMB`, `capacityAllowedMB`.
- [ ] 8.5 Register the cron in `vercel.json`: `{"crons": [{"path": "/api/cron/instance-health", "schedule": "* * * * *"}]}`.
- [ ] 8.6 Build instance registration UI at `apps/web/app/(app)/orgs/[orgId]/instances/new/page.tsx`: form fields (name, base URL, bearer token); submit calls `instance.register`; shows test-connection result before submitting.
- [ ] 8.7 Build instance list page at `apps/web/app/(app)/orgs/[orgId]/instances/page.tsx` showing status badge (HEALTHY/DEGRADED/UNREACHABLE/UNKNOWN), version, and capacity bar.

## 9. Setup wizard

- [ ] 9.1 Create `packages/api/src/lib/setup-state.ts` with `getSetupState(db)` that returns `{ firstUserDone, firstOrgDone, firstInstanceDone }` by querying `User`, `Organization`, and `ControlaiInstance` counts.
- [ ] 9.2 Create middleware in `apps/web/middleware.ts` that redirects unauthenticated users to `/sign-in` and users with no org to `/setup`.
- [ ] 9.3 Build setup wizard at `apps/web/app/(setup)/setup/page.tsx` — 4-step state machine modelled on modules_cloud-main's `setup.tsx`:
  - Step 1: Welcome + sign-up (if no user)
  - Step 2: Create first Org (name + slug)
  - Step 3: Register first ControlaiInstance (URL + token, test connection)
  - Step 4: Done — redirect to dashboard
- [ ] 9.4 Persist current step in URL search params (`?step=1`) so the wizard survives page refreshes.
- [ ] 9.5 Add `GET /api/setup-state` route that returns the setup state JSON; setup wizard polls this to determine which step to show.

## 10. UI pages — Org, Project, SiteGroup, Site CRUD

- [ ] 10.1 Build Org settings page at `apps/web/app/(app)/orgs/[orgId]/settings/page.tsx` with tabs: General (rename), Members (list + role-badge + remove button), Invitations (pending list + invite form), Danger (delete org — OWNER only).
- [ ] 10.2 Build Project list page at `apps/web/app/(app)/orgs/[orgId]/projects/page.tsx`: card grid of projects (name, instance badge, site-group count); "New Project" button opens shadcn Dialog with name + instanceId select.
- [ ] 10.3 Build Project detail page at `apps/web/app/(app)/orgs/[orgId]/projects/[projectId]/page.tsx`: breadcrumb, SiteGroup list as cards, "New Site Group" button.
- [ ] 10.4 Build SiteGroup detail page at `apps/web/app/(app)/orgs/[orgId]/projects/[projectId]/site-groups/[siteGroupId]/page.tsx`: site list table (name, status, provisioned badge), links to Canvas and Dashboard tabs.
- [ ] 10.5 Build Site create/edit drawer at `apps/web/components/domain/site-form.tsx`: name field, broker kind select (mosquitto/EMQX), throughput select (low/mid), ingest direction select (uni/bi), retention select (1m/1h/1d/7d/30d); submits `site.create` or `site.update` tRPC mutation.
- [ ] 10.6 Add delete confirmation dialog `apps/web/components/domain/delete-confirm-dialog.tsx` (reusable): renders resource name + "This action cannot be undone" + confirm button; used by Org, Project, SiteGroup, and Site delete actions.
- [ ] 10.7 Add breadcrumb navigation component `apps/web/components/layout/breadcrumb.tsx` using Next.js 16 App Router segment params; renders `Org / Project / SiteGroup / Canvas` path with links.

## 11. shadcn/ui and component setup

- [ ] 11.1 Initialize shadcn/ui in `apps/web`: `npx shadcn@latest init` with `style: default`, `baseColor: slate`, `cssVariables: true`; creates `components/ui/` and `lib/utils.ts`.
- [ ] 11.2 Add shadcn components used by skeleton pages: `pnpm dlx shadcn@latest add button input label card badge dialog dropdown-menu separator skeleton toast avatar`.
- [ ] 11.3 Create `apps/web/components/layout/app-shell.tsx` — top navigation bar (logo, org-switcher dropdown, user-menu) + left sidebar (Projects tree, Instances link, Settings) + main content area; used by all `(app)` layout pages.
- [ ] 11.4 Add `apps/web/app/(app)/layout.tsx` that wraps children with `<AppShell>` and fetches the current org via `getSession` + `org.list` Server Component call.
- [ ] 11.5 Create global error boundary `apps/web/app/error.tsx` and `apps/web/app/global-error.tsx` following Next.js 16 App Router conventions; display a friendly "Something went wrong" card with a reload button.

## 12. CI & testing

- [ ] 12.1 Create `.github/workflows/ci.yml` with jobs: `lint` (turbo run lint), `typecheck` (turbo run typecheck), `unit-test` (turbo run test — Vitest), `e2e` (Playwright, only on PR to `main`).
- [ ] 12.2 Install Vitest in `packages/api` and `packages/db`: `pnpm add -D vitest @vitest/coverage-v8 -w --filter @controlai-web/api --filter @controlai-web/db`.
- [ ] 12.3 Write unit tests for `packages/api/src/lib/crypto.ts`: round-trip encrypt/decrypt, missing key throws, tampered ciphertext throws.
- [ ] 12.4 Write unit tests for `packages/api/src/routers/instance.ts` (mock `daemon-client`): `register` calls testConnection, inserts with encrypted token; `instance.delete` blocked if projects exist.
- [ ] 12.5 Write unit tests for `packages/api/src/lib/setup-state.ts`: empty DB returns all false; after seed returns correct states.
- [ ] 12.6 Install Playwright: `pnpm add -D @playwright/test -w --filter apps/web`; `pnpm playwright install chromium`.
- [ ] 12.7 Write Playwright E2E test `apps/web/e2e/setup-wizard.spec.ts`: navigate to `/`, expect redirect to `/setup`, complete steps 1-3 with test credentials + mocked daemon, assert redirect to dashboard.
- [ ] 12.8 Configure Vercel preview deployments: add `vercel.json` with `buildCommand`, `outputDirectory`, `framework: nextjs`; set env var secrets in Vercel dashboard.
- [ ] 12.9 Add `openspec validate add-controlai-web-skeleton --strict` step to CI workflow.
- [ ] 12.10 Write Playwright E2E test `apps/web/e2e/org-member-invite.spec.ts`: sign in as OWNER → invite member email → accept via link → assert MEMBER role in members list.
- [ ] 12.11 Write Playwright E2E test `apps/web/e2e/instance-register.spec.ts`: navigate to Instances → Register → fill URL + token → confirm test-connection result → submit → assert instance appears with HEALTHY badge.
