# ADR-009: Password authentication + admin dashboard

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent), direction by Yan
- Builds on: ADR-001 D6 (auth deferred), ADR-008 (currentuser middleware was env-based)

## Context

ADR-008 introduced the multi-user data model with an env-based
current-user middleware as a stop-gap. With more than one user now
plausible, real auth is required. Yan also asked for an admin
dashboard covering Users + Rules + Dealers — the masters that were
previously editable only via the seeder.

Auth choice (selected after discussion):

- **Inside Stele**, not delegated to a proxy. Self-contained,
  deployable anywhere, no Cloudflare/Tailscale dependency.
- **Password + Argon2id**, with email-based reset. Classic but
  100% offline-capable; SMTP is needed only for the reset flow and
  degrades gracefully when missing.

## Decisions

### D1. Password hashing: Argon2id with OWASP parameters

- Library: `golang.org/x/crypto/argon2`.
- Parameters: memory=64 MiB, iterations=3, parallelism=2,
  salt=16 bytes, hash=32 bytes.
- Hashes stored as `$argon2id$v=19$m=65536,t=3,p=2$<b64salt>$<b64hash>`
  (PHC string format) in `users.password_hash text`.
- Minimum password length 10 characters. No other complexity
  requirement (NIST 800-63B aligned: forced complexity reduces real
  entropy by encouraging predictable substitutions).

### D2. Sessions: server-side, HMAC-signed cookie

- Table `sessions (id uuid PK, user_id uuid FK, created_at,
  expires_at, last_seen_at, ip text, user_agent_hash text)`.
- Cookie value: `<session_id>.<hmac>` where HMAC is HS256 over
  `session_id` with secret `STELE_SESSION_SECRET` (32-byte random,
  env). App refuses to start if the secret is missing or shorter
  than 32 bytes.
- Cookie attributes: HttpOnly, Secure, SameSite=Lax, Path=/,
  Max-Age=30 days (rolling on each request via
  `sessions.last_seen_at` and Set-Cookie refresh).
- Why not JWT: server-side sessions allow instant revocation
  (delete row), keep the cookie tiny, and avoid the JWT-spec
  footguns. We pay one DB SELECT per request; trivial.

### D3. Password reset: signed token, single-use

- Table `password_reset_tokens (token_hash text PK, user_id, expires_at, used_at)`.
- The link contains the random 32-byte token (URL-safe base64);
  the DB stores only its SHA-256 (so a DB leak does not enable
  reset takeovers).
- TTL 1 hour. Single-use: marking `used_at` invalidates further
  attempts.
- Reset email lands via SMTP if configured, else is logged at
  `slog.Warn("password reset link", "to", ..., "url", ...)` so the
  dev/seed flow keeps working.

### D4. Roles: simple admin / ops

- `users.role` already exists from ADR-008. Two values matter for
  auth: `admin` and everything else (= "ops").
- `/admin/*` endpoints check `user.Role == "admin"`. Anyone else
  gets 403.
- Granular RBAC (per-resource permissions) deferred until needed.

### D5. Middleware: cookie session beats env

- `web.AuthMiddleware` replaces `CurrentUserMiddleware`:
  - Reads the session cookie.
  - On miss or invalid cookie: redirect to `/login?return=<url>`
    for `GET`s, return 401 JSON for non-GET (HTMX-friendly).
  - On hit: look up the session, hydrate user, inject user_id
    into ctx (same key as before), refresh `last_seen_at`.
- Public routes (no auth): `GET /login`, `POST /login`,
  `GET /forgot`, `POST /forgot`, `GET /reset`, `POST /reset`,
  `GET /healthz`, `GET /static/*`.
- The env-based fallback (`STELE_DEFAULT_USER_EMAIL`) is removed.
  Tests that need a user create a session directly.

### D6. Brute-force resistance: per-email bucket

- In-memory rate limit: 5 failed logins per email per 15 minutes
  triggers a 60-second back-off. Stored in a sync.Map; lost on
  restart, acceptable at our scale.
- Reset-email throttle: 1 reset per email per minute.
- No CAPTCHA. Single-tenant, single-IP-pool attack surface.

### D7. CSRF via SameSite=Lax + double-submit on admin mutations

- `SameSite=Lax` blocks cross-origin POSTs for the session cookie:
  baseline CSRF defence.
- For mutation endpoints under `/admin/*`, additionally render a
  random per-form token; POST must echo it. Stored in the user's
  session, rotated on logout. (Defence-in-depth; can drop later if
  proven unnecessary.)

### D8. Admin dashboard scope

- `GET /admin` → overview (counts: users, dealers, rules, sessions).
- `GET /admin/users`, `POST /admin/users`, `GET /admin/users/{id}`,
  `POST /admin/users/{id}` (update), `POST /admin/users/{id}/deactivate`.
- `GET /admin/rules`, `POST /admin/rules`, `POST /admin/rules/{id}`,
  `POST /admin/rules/{id}/toggle`.
- `GET /admin/dealers`, `POST /admin/dealers`, `POST /admin/dealers/{code}`.
- No hard delete: a deactivated user keeps their event history.
- Soft-delete on users: `users.deactivated_at timestamptz NULL`,
  added by migration 0006.

### D9. Seeder behaviour

- `cmd/seed` sets `password_hash` for all seeded users using a
  fixed dev password `stele-dev-2026`. Yan gets `role=admin`,
  others `role=ops`. The dev password is logged to stderr at seed
  time so it cannot be forgotten.
- On real-user provisioning (post-seed), an admin invites a user
  via `/admin/users`, which sends a reset link to the user.

## Consequences

- Two new packages (`internal/auth`, `internal/mail`) and one
  middleware swap. Bounded blast radius.
- The dev password in seeded prod is a known credential. Acceptable
  for synthetic data; Yan can change it after first login. Document
  in the admin onboarding section of the README.
- SMTP becomes a soft dependency. If unset, reset still works but
  the link must be read from the server logs. Useful for dev.
- The replay sub-command does not need auth: it runs as `system`.
  Document the gap so it isn't mistaken for an auth bypass.

## Open questions deferred

- 2FA (TOTP / WebAuthn): not at M4. Add when an external user with
  elevated privileges joins.
- "Logged in devices" list / per-session revocation UI: not at M4.
- Audit log for admin actions (who edited which rule, when): would
  pair naturally with the event store; deferred until needed.
- Session "remember me" toggle: cookie is already 30-day rolling;
  add explicit toggle only if a shorter default makes sense.
