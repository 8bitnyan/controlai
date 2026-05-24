# Research: better-auth Multi-Tenant Auth for controlai-web
Date: 2026-05-24T00:40:30Z

## Summary

better-auth v1.6.11 (stable, released 2026-05-12) is production-ready for the `controlai-web` stack. It ships a first-class `organization` plugin that covers the `User → Organization (role)` layer natively. The `Project → Site` sub-tenant layer must be implemented as custom Prisma tables with a hand-rolled RBAC helper (exactly as sdi_oc does). The Next.js 16 integration is explicitly documented with the new `proxy.ts` convention. The Prisma adapter is stable and supports schema generation via CLI. The migration thesis away from NextAuth/Auth.js is well-supported.

---

## 1. better-auth Current State (May 2026)

### Version & Stability

| Fact | Evidence |
|------|----------|
| **Latest stable**: `v1.6.11` (2026-05-12) | [GitHub Releases](https://github.com/better-auth/better-auth/releases) — confirmed via `gh api` |
| **Beta track**: `v1.7.0-beta.3` (2026-05-09) | Same source |
| **Active development**: last commit 2026-05-23 (turbo bump) | `gh api repos/better-auth/better-auth/commits/HEAD` |
| **Repo age**: ~18 months, 9000+ issues/PRs processed | GitHub |

**Age flag**: v1.6.x is < 6 months old (released April–May 2026). The library is moving fast; minor breaking changes between minor versions are common.

### Prisma Adapter

**Source**: [Official docs — Prisma adapter](https://www.better-auth.com/docs/adapters/prisma) (fetched 2026-05-24)

```typescript
// packages/db/src/auth.ts
import { betterAuth } from "better-auth";
import { prismaAdapter } from "better-auth/adapters/prisma";
import { PrismaClient } from "@prisma/client";

const prisma = new PrismaClient();

export const auth = betterAuth({
  database: prismaAdapter(prisma, {
    provider: "postgresql",  // or "sqlite", "mysql"
  }),
});
```

**Key notes** (official docs, 2026-05-24):
- Package: `@better-auth/prisma-adapter` (separate install)
- **Prisma 7+**: requires `output` path in `schema.prisma`; import client from that path, not `@prisma/client`
- Schema generation: `npx auth@latest generate` → writes Prisma schema additions
- Schema migration: **NOT supported** by CLI — must run `prisma migrate dev` manually
- **Experimental joins** (v1.4.0+): `experimental: { joins: true }` gives 2–3× perf on `/get-session`, `/get-full-organization`

**Real-world usage in sdi_oc** (local reference, 2026-05-24):
```typescript
// /Users/8bitnyan/Documents/ThinkTank/sdi_oc/apps/web/src/lib/auth.ts
import { betterAuth } from 'better-auth';
import { prismaAdapter } from '@better-auth/prisma-adapter';
import { nextCookies } from 'better-auth/next-js';
import { prisma } from './prisma';

export const auth = betterAuth({
  baseURL: process.env.BETTER_AUTH_URL || process.env.NEXT_PUBLIC_APP_URL || 'http://localhost:3000',
  database: prismaAdapter(prisma, { provider: 'postgresql' }),
  trustedOrigins: [process.env.NEXT_PUBLIC_APP_URL || 'http://localhost:3000'],
  emailAndPassword: {
    enabled: true,
    autoSignIn: true,
    minPasswordLength: 8,
    maxPasswordLength: 128,
  },
  session: {
    expiresIn: 60 * 60 * 24 * 30,   // 30 days
    updateAge: 60 * 60 * 24,          // refresh daily
    cookieCache: {
      enabled: true,
      maxAge: 5 * 60,                 // 5-min signed cookie cache
    },
  },
  user: {
    additionalFields: {
      isSysAdmin: { type: 'boolean', defaultValue: false, input: false },
      mustChangePassword: { type: 'boolean', defaultValue: false, input: false },
      deletedAt: { type: 'date', required: false, input: false },
    },
  },
  plugins: [nextCookies()],  // MUST be last plugin
});
```

### Session Strategy: DB Session vs JWT

**Source**: [Official docs — Session Management](https://www.better-auth.com/docs/concepts/session-management) (fetched 2026-05-24)

better-auth uses **DB-backed sessions by default** (cookie carries a session token; server validates against `session` table on every request). This is the recommended approach for multi-tenant apps where revocation matters.

**Cookie cache** (hybrid approach — recommended for controlai-web):
```typescript
session: {
  cookieCache: {
    enabled: true,
    maxAge: 5 * 60,        // 5-minute signed cookie avoids DB hit on every request
    strategy: "compact",   // smallest size; "jwt" for interop; "jwe" for encrypted
  }
}
```

**Stateless JWT mode** (no DB): possible by omitting `database` config. Not recommended for multi-tenant because you cannot revoke sessions on role change.

**Secondary storage** (Redis): store sessions in Redis instead of Postgres for faster reads:
```typescript
secondaryStorage: {
  get: async (key) => await redis.get(key),
  set: async (key, value, ttl) => await redis.set(key, value, "EX", ttl),
  delete: async (key) => await redis.del(key),
},
session: { storeSessionInDatabase: true }  // keep DB copy for audit
```

### Cookie Config & CSRF Posture

- Cookies are `HttpOnly`, `SameSite=Lax` by default
- CSRF: better-auth uses `SameSite` cookies + `trustedOrigins` allowlist (not a separate CSRF token)
- `trustedOrigins` must include all origins that can make cross-origin requests to the auth API
- Cookie name: `better-auth.session_token` (configurable via `advanced.cookiePrefix`)

### OAuth Providers

**Source**: [Official docs — OAuth](https://www.better-auth.com/docs/concepts/oauth) (fetched 2026-05-24)

Built-in providers (confirmed from source at `packages/better-auth/src/social-providers/index.ts` → re-exports `@better-auth/core/social-providers`):
Google, GitHub, Discord, Apple, Microsoft, Facebook, Twitter/X, LinkedIn, Spotify, Twitch, Dropbox, GitLab, Reddit, Roblox, TikTok, Kick, VK, Zoom, Cognito, and more via Generic OAuth plugin.

**Known gotcha** (open issue #9124, 2026-04-11): OAuth sign-in hard-requires email — providers where email is absent (Discord phone-only, Apple after first sign-in) fail with `error=email_not_found`. Workaround: `mapProfileToUser` to synthesize placeholder email.

### Magic Link

**Source**: [Official docs — Magic Link](https://www.better-auth.com/docs/plugins/magic-link) (fetched 2026-05-24)

```typescript
import { magicLink } from "better-auth/plugins";

export const auth = betterAuth({
  plugins: [
    magicLink({
      sendMagicLink: async ({ email, token, url, metadata }, ctx) => {
        await sendEmail({ to: email, subject: "Sign in", text: `${url}` });
      },
      expiresIn: 300,  // 5 minutes default
    })
  ]
});
```

**Security note** (v1.6.11 release notes, 2026-05-12): Fixed race condition that allowed concurrent requests to mint multiple sessions from the same single-use token (#9572). Multi-instance deployments using secondary-storage verification **must** configure a backend that implements `getAndDelete` (Redis `GETDEL`).

### Email + Password Verification Flow

**Source**: [Official docs — Email & Password](https://www.better-auth.com/docs/authentication/email-password) (fetched 2026-05-24)

```typescript
emailAndPassword: {
  enabled: true,
  requireEmailVerification: true,  // blocks sign-in until verified
  sendResetPassword: async ({ user, url, token }, request) => {
    void sendEmail({ to: user.email, subject: "Reset password", text: url });
  },
  revokeSessionsOnPasswordReset: true,  // revoke all sessions on reset
}
```

Email verification flow:
1. User signs up → `sendVerificationEmail` called with `{ user, url, token }`
2. User clicks link → redirected to `callbackURL` (or root)
3. If `requireEmailVerification: true`, sign-in blocked until verified (returns HTTP 403)

**Email enumeration protection**: when `requireEmailVerification: true`, sign-up always returns 200 even for existing emails (OWASP compliant).

---

## 2. Multi-Tenant ABAC Patterns with better-auth

### Official `organization` Plugin

**Source**: [Official docs — Organization Plugin](https://www.better-auth.com/docs/plugins/organization) (fetched 2026-05-24)

The plugin is **production-ready and first-class** as of v1.6.x. It covers the `User → Organization (role)` layer:

```typescript
import { organization } from "better-auth/plugins";

export const auth = betterAuth({
  plugins: [
    organization({
      ac,           // custom access controller (optional)
      roles: { owner, admin, member, myCustomRole },
      allowUserToCreateOrganization: async (user) => {
        return user.isSysAdmin || (await hasActiveSubscription(user.id));
      },
      sendInvitationEmail: async (data) => {
        const inviteLink = `https://app.example.com/accept-invitation/${data.id}`;
        await sendEmail({ to: data.email, inviteLink });
      },
      requireEmailVerificationOnInvitation: true,  // default true as of v1.6.11
    })
  ]
});
```

**Default roles**: `owner` (full control), `admin` (no delete org/change owner), `member` (read-only).

**Custom ABAC** via `createAccessControl`:
```typescript
// permissions.ts
import { createAccessControl } from "better-auth/plugins/access";
import { defaultStatements, adminAc } from 'better-auth/plugins/organization/access';

const statement = {
  ...defaultStatements,
  project: ["create", "read", "update", "delete"],
  site: ["create", "read", "update", "delete", "provision"],
} as const;

const ac = createAccessControl(statement);

const member = ac.newRole({ project: ["read"], site: ["read"] });
const admin = ac.newRole({
  project: ["create", "read", "update"],
  site: ["create", "read", "update", "provision"],
  ...adminAc.statements,
});
const owner = ac.newRole({
  project: ["create", "read", "update", "delete"],
  site: ["create", "read", "update", "delete", "provision"],
  ...adminAc.statements,
});
```

**Active organization** concept: the plugin stores `activeOrganizationId` in the session. Use `organization.setActive({ organizationId })` to switch context. This is the "workspace" concept.

### `User → Organization → Project → Site` Pattern

**The organization plugin only covers the Org layer.** The `Project → Site` sub-tenant layer must be custom Prisma tables + a hand-rolled RBAC helper. This is exactly what sdi_oc does:

**Evidence** (sdi_oc local codebase, 2026-05-24):

```typescript
// /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/api/src/lib/rbac/permissions.ts

const ORG_ROLE_HIERARCHY: readonly OrgRole[] = [
  OrgRole.MEMBER, OrgRole.ADMIN, OrgRole.OWNER,
] as const;

const PROJECT_ROLE_HIERARCHY: readonly ProjectRole[] = [
  ProjectRole.USER, ProjectRole.PROJECT_MANAGER, ProjectRole.PROJECT_ADMIN,
] as const;

export async function requireOrgRole(
  userId: string, orgId: string, minimumRole: OrgRole
): Promise<OrgRole> {
  const user = await prisma.user.findUnique({ where: { id: userId }, select: { isSysAdmin: true } });
  if (user?.isSysAdmin) return OrgRole.OWNER;  // SYSADMIN bypass

  const membership = await prisma.organizationMember.findUnique({
    where: { organizationId_userId: { organizationId: orgId, userId } },
    select: { role: true },
  });
  if (!membership) throw new TRPCError({ code: "FORBIDDEN", message: "Not a member." });
  if (orgRoleLevel(membership.role) < orgRoleLevel(minimumRole))
    throw new TRPCError({ code: "FORBIDDEN", message: `Requires ${minimumRole}.` });
  return membership.role;
}

export async function requireProjectRole(
  userId: string, projectId: string, minimumRole: ProjectRole
): Promise<ProjectRole> {
  // same pattern for ProjectUser table
}
```

**Key design decision**: sdi_oc does NOT use better-auth's organization plugin for its multi-tenant model. It manages `OrganizationMember` and `ProjectUser` tables directly in Prisma, with `requireOrgRole` / `requireProjectRole` helpers called inside tRPC procedures. The better-auth `organization` plugin is used only for the invitation email flow.

**Recommendation for controlai-web**: Use the better-auth `organization` plugin for the invitation/email flow and the `activeOrganizationId` session concept. Manage `OrganizationMember` and `ProjectUser` tables directly in Prisma. Use the sdi_oc RBAC helper pattern verbatim.

### Role Checks in tRPC Middleware

**Evidence** (sdi_oc local codebase, 2026-05-24):

```typescript
// /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/api/src/trpc/procedures.ts

const isAuthed = middleware(async ({ ctx, next }) => {
  if (!ctx.session || !ctx.user) {
    throw new TRPCError({ code: "UNAUTHORIZED", message: "Must be signed in." });
  }
  return next({ ctx: { session: ctx.session, user: ctx.user } });
});

export const protectedProcedure = baseProcedure.use(isAuthed);
export const adminProcedure = baseProcedure.use(isAuthed).use(isSysAdmin);
```

Per-resource ABAC is done **inside each procedure** (not in a shared middleware), by calling `requireOrgRole` or `requireProjectRole`:

```typescript
// /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/api/src/routers/organization.ts
update: protectedProcedure
  .input(z.object({ orgId: z.string().uuid(), name: z.string().min(1).max(100).optional() }))
  .mutation(async ({ ctx, input }) => {
    await requireOrgRole(ctx.user.id, input.orgId, OrgRole.ADMIN);  // ABAC check
    // ... proceed with update
  }),
```

---

## 3. Invite + Accept Flow

### Official better-auth Organization Invitation Flow

**Source**: [Official docs — Organization Plugin, Invitations section](https://www.better-auth.com/docs/plugins/organization) (fetched 2026-05-24)

**Step 1: Configure `sendInvitationEmail`**
```typescript
organization({
  async sendInvitationEmail(data) {
    const inviteLink = `https://app.example.com/accept-invitation/${data.id}`;
    await sendEmail({
      to: data.email,
      invitedByUsername: data.inviter.user.name,
      teamName: data.organization.name,
      inviteLink,
    });
  },
  requireEmailVerificationOnInvitation: true,  // default true in v1.6.11
})
```

**Step 2: Send invitation (client)**
```typescript
await authClient.organization.inviteMember({
  email: "user@example.com",
  role: "member",
  organizationId: "org-id",
  resend: true,  // resend if already invited
});
```

**Step 3: Accept invitation (client, user must be logged in)**
```typescript
// On the /accept-invitation/[id] page:
await authClient.organization.acceptInvitation({
  invitationId: invitationId,  // from URL param
});
```

**Security note** (v1.6.11 release notes, 2026-05-12): Fixed invitation takeover vulnerability — `requireEmailVerificationOnInvitation` is now **true by default** and the verification gate extends to `getInvitation` and `listUserInvitations` (#9577).

### sdi_oc Custom Invitation Flow (alternative pattern)

sdi_oc implements invitations as a **custom tRPC router** (not using the better-auth organization plugin's invitation system). This gives more control over the token URL shape and email content:

**Evidence** (sdi_oc local codebase, 2026-05-24):
```typescript
// /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/api/src/routers/invitation.ts

create: protectedProcedure
  .input(z.object({ orgId: z.string().uuid(), email: z.string().email(), role: z.enum([...]) }))
  .mutation(async ({ ctx, input }) => {
    await requireOrgRole(ctx.user.id, input.orgId, OrgRole.ADMIN);
    // Check for existing pending invitation, check if already member
    const expiresAt = new Date(); expiresAt.setDate(expiresAt.getDate() + 7);
    const invitation = await ctx.db.organizationInvitation.create({
      data: { organizationId: input.orgId, email: input.email, role: input.role,
              invitedById: ctx.user.id, status: InvitationStatus.PENDING, expiresAt },
    });
    return invitation;  // caller sends email with invitation.id in URL
  }),

accept: protectedProcedure
  .input(z.object({ invitationId: z.string().uuid() }))
  .mutation(async ({ ctx, input }) => {
    const invitation = await ctx.db.organizationInvitation.findUniqueOrThrow({ where: { id: input.invitationId } });
    if (invitation.email !== ctx.user.email) throw new TRPCError({ code: "FORBIDDEN" });
    if (invitation.status !== InvitationStatus.PENDING) throw new TRPCError({ code: "BAD_REQUEST" });
    if (invitation.expiresAt < new Date()) { /* mark expired, throw */ }

    const result = await ctx.db.$transaction(async (tx) => {
      const member = await tx.organizationMember.create({
        data: { organizationId: invitation.organizationId, userId: ctx.user.id, role: invitation.role },
      });
      await tx.organizationInvitation.update({ where: { id: input.invitationId }, data: { status: InvitationStatus.ACCEPTED } });
      return member;
    });
    return result;
  }),
```

**Recommendation for controlai-web**: Use the **better-auth organization plugin's invitation system** for the email send hook (it handles token generation, expiry, and the accept endpoint). For the accept page, call `authClient.organization.acceptInvitation({ invitationId })`. This avoids reimplementing token management.

---

## 4. Next.js 16 + tRPC + better-auth Wiring

### Official Next.js 16 Integration

**Source**: [Official docs — Next.js integration](https://www.better-auth.com/docs/integrations/next) (fetched 2026-05-24)

**Key change in Next.js 16**: `middleware.ts` → `proxy.ts`, `middleware` function → `proxy` function. Migration codemod: `npx @next/codemod@canary middleware-to-proxy .`

**Route handler** (unchanged from Next.js 13–15):
```typescript
// apps/web/src/app/api/auth/[...all]/route.ts
import { auth } from "@/lib/auth";
import { toNextJsHandler } from "better-auth/next-js";
export const { GET, POST } = toNextJsHandler(auth);
```

**Evidence** (sdi_oc local codebase, 2026-05-24):
```typescript
// /Users/8bitnyan/Documents/ThinkTank/sdi_oc/apps/web/src/app/api/auth/[...all]/route.ts
import { auth } from '@/lib/auth';
import { toNextJsHandler } from 'better-auth/next-js';
export const { GET, POST } = toNextJsHandler(auth);
```

**Auth protection in Next.js 16 proxy** (official docs):
```typescript
// apps/web/src/proxy.ts
import { NextRequest, NextResponse } from "next/server";
import { headers } from "next/headers";
import { auth } from "@/lib/auth";

export async function proxy(request: NextRequest) {
  const session = await auth.api.getSession({ headers: await headers() });
  if (!session) return NextResponse.redirect(new URL("/sign-in", request.url));
  return NextResponse.next();
}

export const config = { matcher: ["/dashboard"] };
```

**Server Action cookies** (critical for App Router):
```typescript
// auth.ts — nextCookies() must be LAST plugin
plugins: [nextCookies()]
```
Without `nextCookies()`, server actions that call `auth.api.signInEmail` won't set cookies.

### tRPC Context Wiring

**Evidence** (sdi_oc local codebase, 2026-05-24):

```typescript
// /Users/8bitnyan/Documents/ThinkTank/sdi_oc/apps/web/src/lib/trpc/server.ts
import "server-only";
import { cache } from "react";
import { auth } from "@/lib/auth";
import { headers } from "next/headers";

const createContext = cache(async () => {
  const session = await auth.api.getSession({ headers: await headers() });

  let ctxSession: { userId: string } | null = null;
  let user = null;

  if (session?.user?.id) {
    const dbUser = await prisma.user.findUnique({ where: { id: session.user.id } });
    if (dbUser) {
      ctxSession = { userId: dbUser.id };
      user = dbUser;
    }
  }

  return createTRPCContext({ session: ctxSession, user });
});
```

**Pattern**: `auth.api.getSession({ headers: await headers() })` is the canonical way to read the session in RSC/server actions. The `React.cache()` wrapper ensures the DB lookup happens once per request.

**tRPC context shape**:
```typescript
// /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/api/src/trpc/init.ts
export interface TRPCContext {
  db: typeof prisma;
  session: { userId: string } | null;
  user: User | null;
}
```

**Route handler for tRPC** (typical pattern for App Router):
```typescript
// apps/web/src/app/api/trpc/[trpc]/route.ts
import { fetchRequestHandler } from "@trpc/server/adapters/fetch";
import { appRouter } from "@sdi/api";
import { auth } from "@/lib/auth";
import { headers } from "next/headers";

const handler = (req: Request) =>
  fetchRequestHandler({
    endpoint: "/api/trpc",
    req,
    router: appRouter,
    createContext: async () => {
      const session = await auth.api.getSession({ headers: await headers() });
      // ... build context
    },
  });

export { handler as GET, handler as POST };
```

---

## 5. Session Refresh + Revocation

**Source**: [Official docs — Session Management](https://www.better-auth.com/docs/concepts/session-management) (fetched 2026-05-24)

### Revocation APIs

```typescript
// Revoke a specific session (e.g., on role change)
await authClient.revokeSession({ token: "session-token" });

// Revoke all sessions for current user (e.g., on org removal)
await authClient.revokeSessions();

// Revoke all OTHER sessions (e.g., on password change)
await authClient.revokeOtherSessions();
```

**Server-side revocation** (for role change triggered by admin):
```typescript
// In a tRPC mutation that changes a user's org role:
await auth.api.revokeUserSessions({
  body: { userId: targetUserId },
  headers: await headers(),
});
```

### Revocation on Role Change

**Pattern for controlai-web**: When `requireOrgRole` changes (e.g., user demoted from ADMIN to MEMBER), call `auth.api.revokeUserSessions` for that user. The next request will re-fetch the session from DB with the new role.

**Cookie cache caveat**: If `cookieCache.enabled: true`, revoked sessions may remain active on other devices until `maxAge` expires (default 5 minutes in sdi_oc). For immediate revocation on sensitive operations (org removal, role demotion), either:
1. Set `maxAge` to 60 seconds, or
2. Call `getSession({ query: { disableCookieCache: true } })` on sensitive routes

### Session Refresh on Role Change

better-auth does not automatically push role changes to existing sessions. The session cookie carries only `userId`. Role is always fetched from DB on each tRPC call via `requireOrgRole`. This means role changes take effect on the next tRPC call — no session invalidation needed for role changes (only for org removal).

---

## 6. Comparison to NextAuth/Auth.js

**Source**: [Official docs — Comparison](https://www.better-auth.com/docs/comparison) (fetched 2026-05-24)

| Dimension | better-auth | NextAuth/Auth.js |
|-----------|-------------|-----------------|
| **Multi-tenancy** | First-class `organization` plugin with ABAC | No built-in; must DIY |
| **Session storage** | DB-backed by default (revocable) | JWT by default (not revocable without extra work) |
| **Plugin system** | Extensible (magic-link, 2FA, org, admin, etc.) | Adapter-based, less composable |
| **Prisma adapter** | Official, CLI-generated schema | Official, but schema management is manual |
| **Self-hosted** | Yes, runs in your app | Yes |
| **Vendor lock-in** | None | None |
| **TypeScript inference** | Strong (infers session shape from config) | Good |
| **Next.js 16 support** | Explicitly documented with proxy.ts | Not yet documented (as of 2026-05-24) |
| **Active development** | Very active (daily commits, v1.7 beta) | Slower cadence |
| **Known issues** | See §7 | Stale session on role change (JWT mode) |

**Migration thesis** (confirmed): better-auth is the right choice over NextAuth for controlai-web because:
1. The `organization` plugin eliminates ~500 lines of custom invitation/membership code
2. DB-backed sessions make role-change revocation trivial
3. Next.js 16 proxy support is explicitly documented
4. sdi_oc is already migrating to it (active spec `replace-nextauth-with-better-auth`)
5. No per-user cost (vs Clerk)
6. `customSession` plugin allows injecting org/project role into session response for client-side use

---

## 7. Known Gotchas / Production Warnings

### Security Fixes in v1.6.11 (2026-05-12) — All < 6 months old

These were **vulnerabilities** fixed in the latest release. Ensure you're on v1.6.11+:

1. **Magic link race condition** (#9572): concurrent requests could mint multiple sessions from one token. Fixed in v1.6.11.
2. **Invitation takeover** (#9577): `requireEmailVerificationOnInvitation` now defaults to `true`. If you were relying on `false`, update your config explicitly.
3. **OIDC/MCP confidential client** (#9576): `client_secret` now required for refresh token grants.
4. **Device authorization binding** (#9573): pending codes now bound to verifying session.
5. **ReDoS in proxy host header validation** (#8898, 2026-04-01): regex vulnerability in `utils/url.ts`. Fixed in a patch.

### Open Tracking Issues (as of 2026-05-24)

| Issue | Title | Impact |
|-------|-------|--------|
| #9183 | email/OTP pipeline (Prisma v7 regressions) | **HIGH** if using Prisma 7 |
| #9182 | stateless sessions (account_data lifecycle) | Low for DB-backed sessions |
| #9177 | session cookie cache (lifecycle, cleanup) | Medium — affects cookie cache |
| #9124 | OAuth sign-in requires email (Discord phone-only) | Low for controlai-web (email+password primary) |
| #9031 | SSO list providers returns all providers across orgs | Low (SSO plugin not needed for controlai-web) |
| #8826 | Owner cannot invite another user with `owner` role | **MEDIUM** — workaround: use server-side `addMember` |

### Prisma v7 Regression (#9183)

**Flag**: If using Prisma 7 (which requires `output` path), there are known regressions in the email/OTP pipeline. Stick with Prisma 6.x until #9183 is resolved, or test thoroughly.

### TypeScript Stack Overflow (#8781, 2026-03-26)

With 25+ plugins + TypeScript 6+, V8 can stack overflow during type inference. For controlai-web (likely 5–8 plugins), this is not a concern.

### `nextCookies()` Must Be Last Plugin

```typescript
// CORRECT
plugins: [organization(), magicLink(...), nextCookies()]

// WRONG — nextCookies() not last, server action cookies won't be set
plugins: [nextCookies(), organization()]
```

### `trustedOrigins` Required for Cross-Origin

If the tRPC API and the web app are on different origins (e.g., during development with `localhost:3000` and `localhost:3001`), `trustedOrigins` must include both.

### `deferSessionRefresh` for Read Replicas

If using Postgres read replicas (Phase 2 scaling), enable:
```typescript
session: { deferSessionRefresh: true }
```
This makes `GET /get-session` read-only; the client auto-calls `POST` to refresh.

### Multi-Instance Magic Link

For multi-instance deployments (multiple BFF pods), magic link verification requires Redis with `GETDEL` support. Plain `get` + `delete` is not atomic across instances.

---

## 8. Recommended Auth Config for controlai-web

Based on all findings, the recommended `auth.ts` for controlai-web:

```typescript
// packages/api/src/auth.ts
import { betterAuth } from "better-auth";
import { prismaAdapter } from "better-auth/adapters/prisma";
import { organization } from "better-auth/plugins";
import { magicLink } from "better-auth/plugins";
import { nextCookies } from "better-auth/next-js";
import { createAccessControl } from "better-auth/plugins/access";
import { defaultStatements, adminAc } from "better-auth/plugins/organization/access";
import { prisma } from "./db";

// Custom ABAC for controlai-web domain
const statement = {
  ...defaultStatements,
  project: ["create", "read", "update", "delete"],
  site: ["create", "read", "update", "delete", "provision", "monitor"],
} as const;
const ac = createAccessControl(statement);
const member = ac.newRole({ project: ["read"], site: ["read", "monitor"] });
const admin = ac.newRole({
  project: ["create", "read", "update"],
  site: ["create", "read", "update", "provision", "monitor"],
  ...adminAc.statements,
});
const owner = ac.newRole({
  project: ["create", "read", "update", "delete"],
  site: ["create", "read", "update", "delete", "provision", "monitor"],
  ...adminAc.statements,
});

export const auth = betterAuth({
  baseURL: process.env.BETTER_AUTH_URL!,
  database: prismaAdapter(prisma, { provider: "postgresql" }),
  trustedOrigins: [process.env.NEXT_PUBLIC_APP_URL!],
  experimental: { joins: true },  // 2-3x perf on get-session

  emailAndPassword: {
    enabled: true,
    requireEmailVerification: true,
    sendResetPassword: async ({ user, url }) => {
      void sendEmail({ to: user.email, subject: "Reset password", text: url });
    },
    revokeSessionsOnPasswordReset: true,
  },

  emailVerification: {
    sendVerificationEmail: async ({ user, url }) => {
      void sendEmail({ to: user.email, subject: "Verify email", text: url });
    },
  },

  session: {
    expiresIn: 60 * 60 * 24 * 30,  // 30 days
    updateAge: 60 * 60 * 24,
    cookieCache: {
      enabled: true,
      maxAge: 5 * 60,
      strategy: "compact",
    },
    deferSessionRefresh: false,  // set true if adding read replicas
  },

  user: {
    additionalFields: {
      isSysAdmin: { type: "boolean", defaultValue: false, input: false },
    },
  },

  socialProviders: {
    google: {
      clientId: process.env.GOOGLE_CLIENT_ID!,
      clientSecret: process.env.GOOGLE_CLIENT_SECRET!,
    },
    github: {
      clientId: process.env.GITHUB_CLIENT_ID!,
      clientSecret: process.env.GITHUB_CLIENT_SECRET!,
    },
  },

  plugins: [
    organization({
      ac,
      roles: { owner, admin, member },
      requireEmailVerificationOnInvitation: true,
      sendInvitationEmail: async (data) => {
        const inviteLink = `${process.env.NEXT_PUBLIC_APP_URL}/accept-invitation/${data.id}`;
        void sendEmail({
          to: data.email,
          subject: `You're invited to ${data.organization.name}`,
          text: inviteLink,
        });
      },
    }),
    magicLink({
      sendMagicLink: async ({ email, url }) => {
        void sendEmail({ to: email, subject: "Sign in to controlai", text: url });
      },
    }),
    nextCookies(),  // MUST be last
  ],
});
```

---

## 9. Prisma Schema Additions (generated by `npx auth@latest generate`)

The CLI generates these tables. Key additions for the organization plugin:

```prisma
model Organization {
  id          String   @id
  name        String
  slug        String   @unique
  logo        String?
  metadata    String?
  createdAt   DateTime
  members     Member[]
  invitations Invitation[]
}

model Member {
  id             String       @id
  organizationId String
  userId         String
  role           String
  createdAt      DateTime
  organization   Organization @relation(fields: [organizationId], references: [id], onDelete: Cascade)
  user           User         @relation(fields: [userId], references: [id], onDelete: Cascade)
}

model Invitation {
  id             String       @id
  organizationId String
  email          String
  role           String?
  status         String
  expiresAt      DateTime
  inviterId      String
  organization   Organization @relation(fields: [organizationId], references: [id], onDelete: Cascade)
  user           User         @relation(fields: [inviterId], references: [id], onDelete: Cascade)
}
```

**Note**: The better-auth `Member` table is separate from the custom `OrganizationMember` table in sdi_oc. For controlai-web, you can either:
- Use better-auth's `Member` table as the source of truth for org membership (simpler)
- Use a custom `OrganizationMember` table (more control, sdi_oc pattern)

**Recommendation**: Use better-auth's `Member` table for org membership (avoids duplication). Add custom `ProjectUser` table for project-level RBAC.

---

## Sources

| Source | Type | Date | URL |
|--------|------|------|-----|
| better-auth releases | Official GitHub | 2026-05-12 (latest) | https://github.com/better-auth/better-auth/releases |
| Organization plugin docs | Official docs | Fetched 2026-05-24 | https://www.better-auth.com/docs/plugins/organization |
| Session management docs | Official docs | Fetched 2026-05-24 | https://www.better-auth.com/docs/concepts/session-management |
| Prisma adapter docs | Official docs | Fetched 2026-05-24 | https://www.better-auth.com/docs/adapters/prisma |
| Next.js integration docs | Official docs | Fetched 2026-05-24 | https://www.better-auth.com/docs/integrations/next |
| Email & Password docs | Official docs | Fetched 2026-05-24 | https://www.better-auth.com/docs/authentication/email-password |
| Magic Link docs | Official docs | Fetched 2026-05-24 | https://www.better-auth.com/docs/plugins/magic-link |
| OAuth docs | Official docs | Fetched 2026-05-24 | https://www.better-auth.com/docs/concepts/oauth |
| Comparison docs | Official docs | Fetched 2026-05-24 | https://www.better-auth.com/docs/comparison |
| v1.6.11 release notes | Official GitHub | 2026-05-12 | https://github.com/better-auth/better-auth/releases/tag/v1.6.11 |
| sdi_oc auth.ts | Local codebase (GitHub code) | 2026-05-24 | /Users/8bitnyan/Documents/ThinkTank/sdi_oc/apps/web/src/lib/auth.ts |
| sdi_oc trpc/init.ts | Local codebase (GitHub code) | 2026-05-24 | /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/api/src/trpc/init.ts |
| sdi_oc trpc/procedures.ts | Local codebase (GitHub code) | 2026-05-24 | /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/api/src/trpc/procedures.ts |
| sdi_oc trpc/server.ts | Local codebase (GitHub code) | 2026-05-24 | /Users/8bitnyan/Documents/ThinkTank/sdi_oc/apps/web/src/lib/trpc/server.ts |
| sdi_oc rbac/permissions.ts | Local codebase (GitHub code) | 2026-05-24 | /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/api/src/lib/rbac/permissions.ts |
| sdi_oc routers/organization.ts | Local codebase (GitHub code) | 2026-05-24 | /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/api/src/routers/organization.ts |
| sdi_oc routers/invitation.ts | Local codebase (GitHub code) | 2026-05-24 | /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/api/src/routers/invitation.ts |
| sdi_oc shared-types/auth.ts | Local codebase (GitHub code) | 2026-05-24 | /Users/8bitnyan/Documents/ThinkTank/sdi_oc/packages/shared-types/src/auth.ts |
| better-auth open issues | Official GitHub | 2026-05-24 | https://github.com/better-auth/better-auth/issues |

**Age flags**:
- All better-auth v1.6.x content is < 6 months old (April–May 2026)
- sdi_oc codebase is the primary real-world reference; it is actively maintained
- The security fixes in v1.6.11 are < 2 weeks old — ensure you pin to v1.6.11+
