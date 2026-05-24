# controlai-web-auth Specification

## Purpose
TBD - created by archiving change add-controlai-web-skeleton. Update Purpose after archive.
## Requirements
### Requirement: Email and password authentication via better-auth

`controlai-web` SHALL use **better-auth v1.6+** with the Prisma adapter for
authentication. Users SHALL be able to sign up and sign in with email + password.
Email verification SHALL NOT be required (D6 — dev-friendly; accepted risk documented in
design.md). Sessions SHALL be stored in the database via better-auth's DB-backed session
strategy and delivered as signed, HTTP-only cookies.

#### Scenario: Successful sign-up

- **WHEN** a new user submits a valid email and password (minimum 8 characters) on the sign-up form
- **THEN** better-auth SHALL create a `User` and a `Session` row in Postgres
- **AND** the browser SHALL receive a signed HTTP-only session cookie
- **AND** the user SHALL be redirected to the setup wizard if no Org exists, or to the dashboard if one does

#### Scenario: Duplicate email rejected

- **WHEN** a user attempts to sign up with an email address already registered
- **THEN** better-auth SHALL return an error and the sign-up form SHALL display "Email already in use"
- **AND** no new `User` row SHALL be created

#### Scenario: Successful sign-in

- **WHEN** a registered user submits correct credentials on the sign-in form
- **THEN** better-auth SHALL create a new `Session` row and issue a fresh cookie
- **AND** the user SHALL be redirected to the last-visited page or the dashboard

#### Scenario: Wrong password rejected

- **WHEN** a user submits an incorrect password on the sign-in form
- **THEN** better-auth SHALL return an error and the form SHALL display "Invalid credentials"
- **AND** no session SHALL be created

#### Scenario: Sign-out invalidates session

- **WHEN** a signed-in user clicks "Sign out" in the user menu
- **THEN** better-auth SHALL delete the `Session` row from Postgres
- **AND** the browser cookie SHALL be cleared
- **AND** subsequent requests with the old cookie SHALL return 401

### Requirement: Organization plugin — create, invite, and manage members

better-auth SHALL be configured with the **organization plugin** providing Org-scoped
member management. A user SHALL be able to create an Org (becoming OWNER), invite others
by email, and accept or reject invitations. The first user to sign up SHALL automatically
become a sysadmin (no dedicated sysadmin role — handled by setup wizard making them OWNER
of the first Org).

#### Scenario: Create an organization

- **WHEN** an authenticated user submits a new org name and unique slug
- **THEN** better-auth SHALL insert an `Organization` row and an `OrganizationMember` row with role OWNER
- **AND** the user SHALL be redirected to the new org's dashboard

#### Scenario: Slug uniqueness enforced

- **WHEN** a user creates an Org with a slug already taken
- **THEN** the API SHALL return HTTP 409 and display "Slug already taken"

#### Scenario: Invite a member by email

- **WHEN** an Org OWNER or ADMIN sends an invitation to `invitee@example.com`
- **THEN** an `OrganizationInvitation` row SHALL be created with a unique token and 7-day expiry
- **AND** an invitation email SHALL be sent via the configured email provider (Resend or stub)
- **AND** the invitee SHALL be listed as "Pending" in the members list

#### Scenario: Accept invitation

- **WHEN** the invitee clicks the invitation link and is signed in (or signs up)
- **THEN** better-auth SHALL create an `OrganizationMember` row with the role specified in the invitation (default: MEMBER)
- **AND** the invitation token SHALL be marked as used
- **AND** the invitee SHALL be redirected to the org dashboard

#### Scenario: Expired invitation rejected

- **WHEN** an invitee clicks an invitation link more than 7 days after it was issued
- **THEN** the page SHALL display "Invitation expired"
- **AND** no `OrganizationMember` row SHALL be created

### Requirement: Role-based access control (ABAC) via tRPC middleware

The tRPC server SHALL enforce ABAC at the procedure level. Three procedure tiers
SHALL exist: `publicProcedure` (no auth), `protectedProcedure` (valid session required),
`orgProcedure` (valid session + verified org membership), `ownerAdminProcedure`
(valid session + OWNER or ADMIN role in the org). The org membership check SHALL read
the `orgId` from the procedure input, not the session, to support multi-org users.

#### Scenario: Unauthenticated request to protected procedure

- **WHEN** a client calls a `protectedProcedure` without a valid session cookie
- **THEN** the tRPC server SHALL return `TRPCError({ code: 'UNAUTHORIZED' })`
- **AND** the client SHALL redirect the user to the sign-in page

#### Scenario: Non-member accessing org procedure

- **WHEN** an authenticated user calls an `orgProcedure` with an `orgId` they are not a member of
- **THEN** the tRPC server SHALL return `TRPCError({ code: 'FORBIDDEN' })`

#### Scenario: MEMBER blocked from owner-admin procedure

- **WHEN** an authenticated user with role MEMBER calls an `ownerAdminProcedure`
- **THEN** the tRPC server SHALL return `TRPCError({ code: 'FORBIDDEN' })`

#### Scenario: OWNER allowed on owner-admin procedure

- **WHEN** an authenticated user with role OWNER calls an `ownerAdminProcedure`
- **THEN** the procedure SHALL execute and return the result

### Requirement: Session persistence and cookie security

Sessions SHALL be persisted in the Postgres `Session` table via better-auth's DB
adapter. Session cookies SHALL be `HttpOnly`, `Secure` (in production), `SameSite=Lax`,
and SHALL carry a maximum age of 30 days. A cookie cache (in-memory + DB) SHALL be used
to reduce DB reads per request per better-auth's `cookieCache` option.

#### Scenario: Session survives server restart

- **WHEN** the Next.js process is restarted
- **THEN** a user with a valid cookie SHALL remain signed in (session row persists in Postgres)

#### Scenario: Cookie cache reduces DB reads

- **WHEN** a user makes 10 rapid tRPC requests within the cookie cache window
- **THEN** the `Session` table SHALL be queried at most once per cache TTL (default: 60 s)
- **AND** all 10 requests SHALL resolve as authenticated

