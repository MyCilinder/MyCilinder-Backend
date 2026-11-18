# Auth facade

Go service that wraps Amazon Cognito behind the `/auth` and `/me` endpoints of
[`docs/openapi.yaml`](../../docs/openapi.yaml), so the client never uses the Cognito SDK
(README §6.2, risk R6). It owns the `auth.users` profile table and writes auth events to
`audit.audit_log`.

Design references: Architecture §4.2, Interface Design §1.2 and §3.1, Data Design §1.3,
and the component diagram [`c4-level3-auth`](../../docs/diagrams/c4-level3-auth.puml).

## Endpoints

| Route | Auth | Success | Cognito call |
|-------|------|---------|--------------|
| `POST /api/v1/auth/register` | Public | 202 | `SignUp` |
| `POST /api/v1/auth/confirm` | Public | 204 | `ConfirmSignUp` |
| `POST /api/v1/auth/resend-code` | Public | 204 | `ResendConfirmationCode` |
| `POST /api/v1/auth/login` | Public | 200 | `InitiateAuth` (`USER_PASSWORD_AUTH`) |
| `POST /api/v1/auth/refresh` | Public | 200 | `GetTokensFromRefreshToken` |
| `POST /api/v1/auth/logout` | JWT | 204 | `RevokeToken` |
| `GET /api/v1/me` | JWT | 200 | `GetUser` |
| `PATCH /api/v1/me` | JWT | 200 | `GetUser` |
| `GET /healthz`, `GET /readyz` | none | 200 / 503 | none |

Behavior worth knowing:
- Register, confirm, and resend-code do not reveal whether an email is registered.
  Login failures return a generic 401 (UC-01 3a, UC-02 2a).
- The local profile is created on the first successful login, using the Cognito `name`
  attribute. Later logins never overwrite a changed display name.
- `/readyz` checks only the database. Redis is not part of readiness because throttling
  fails open.
- `GET /me` calls Cognito on every request. That call also rejects access tokens revoked
  by logout, which local JWT validation cannot detect.

## Layout

```
cmd/auth/            service entry point (also `auth healthcheck` for container probes)
cmd/cognito-smoke/   dev tool: exercise the Cognito adapter against a real pool
internal/auth/       sign-up, login, refresh, logout use cases; Redis login throttle
internal/profile/    GET and PATCH /me
internal/cognito/    Cognito adapter: SECRET_HASH, error mapping, SDK client
internal/store/      auth.users repository, audit.audit_log writer (pgx)
internal/user/       profile type and display-name rule
internal/http/       router, handlers, request validation, problem mapping
internal/config/     environment configuration
```

The shared pieces (config loading, Problem Details, middleware, JWT validation, rate
limiting, database and Redis clients, health handlers) are in [`libs/go`](../../libs/go).

## Cognito setup (console)

Create one user pool for development and fill `services/auth/.env` from it.

1. **User pool.** Sign-in option: email. Self-registration on. Required attributes:
   `email`, plus `name` (the display name). Email verification required, using the
   Cognito default email sender (limited to 50 emails per day). No MFA.
2. **App client.** Type: confidential, with a client secret. Authentication flows:
   `ALLOW_USER_PASSWORD_AUTH` and `ALLOW_REFRESH_TOKEN_AUTH`. Token revocation on.
   Refresh token rotation off. Default token lifetimes (access and ID tokens 1 hour,
   refresh token 30 days).
3. **Group.** Create `operators`. It is not used by this service, but tokens carry it for
   Catalog and Interactions.
4. Copy the pool id, app client id, and client secret into `.env`:

```sh
cp services/auth/.env.example services/auth/.env
# edit COGNITO_USER_POOL_ID, COGNITO_CLIENT_ID, COGNITO_CLIENT_SECRET
```

No AWS credentials are needed: the operations above are authorized by the client secret
and the user's tokens, not IAM.

## Running locally

From the repository root. Go is not required on the host; every command below has a
Docker form.

```sh
# Postgres, Redis, migrations, and the auth service in a container (port 8000)
docker compose -f deploy/local/docker-compose.yml up -d --build

# or: only the dependencies, then run the service from source
docker compose -f deploy/local/docker-compose.yml up -d postgres redis migrate-audit migrate-auth
set -a; . services/auth/.env; set +a
go run ./services/auth/cmd/auth
```

Stop with `docker compose -f deploy/local/docker-compose.yml down` (add `-v` to delete the
database volume).

### Walkthrough with curl

```sh
B=http://127.0.0.1:8000/api/v1
EMAIL=you@example.com
PASS='Secret#123'

curl -i -X POST $B/auth/register -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\",\"displayName\":\"Diego\"}"
# 202. Cognito emails a 6-digit code.

curl -i -X POST $B/auth/confirm -d "{\"email\":\"$EMAIL\",\"code\":\"123456\"}"
# 204 (or 400 with rule invalid-code / expired-code; then POST /auth/resend-code)

curl -s -X POST $B/auth/login -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\"}" | tee /tmp/tokens.json
AT=$(jq -r .accessToken /tmp/tokens.json); RT=$(jq -r .refreshToken /tmp/tokens.json)

curl -s $B/me -H "Authorization: Bearer $AT"
curl -s -X PATCH $B/me -H "Authorization: Bearer $AT" -d '{"displayName":"Diego G"}'
curl -s -X POST $B/auth/refresh -d "{\"refreshToken\":\"$RT\"}"
curl -i -X POST $B/auth/logout -H "Authorization: Bearer $AT" -d "{\"refreshToken\":\"$RT\"}"
curl -i -X POST $B/auth/refresh -d "{\"refreshToken\":\"$RT\"}"   # now 401
```

After the first login, the profile and audit rows are visible with:

```sh
docker compose -f deploy/local/docker-compose.yml exec postgres \
  psql -U postgres -d mycilinder -c 'select * from auth.users' -c 'select * from audit.audit_log'
```

### Cognito smoke test

`cognito-smoke` calls the adapter directly, without the HTTP layer or database:

```sh
set -a; . services/auth/.env; set +a
SMOKE_PASSWORD='Secret#123' go run ./services/auth/cmd/cognito-smoke signup you@example.com "Diego"
go run ./services/auth/cmd/cognito-smoke confirm you@example.com 123456
SMOKE_PASSWORD='Secret#123' go run ./services/auth/cmd/cognito-smoke login you@example.com
```

## Tests

```sh
# unit tests (no dependencies)
go test ./libs/go/... ./services/auth/...

# integration tests: Postgres and Redis from Compose; Cognito is faked
TEST_DATABASE_URL='postgres://auth_svc:auth_svc_local@127.0.0.1:5432/mycilinder?sslmode=disable' \
TEST_ADMIN_DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5432/mycilinder?sslmode=disable' \
TEST_REDIS_ADDR=127.0.0.1:6379 \
  go test -tags integration ./libs/go/... ./services/auth/...
```

Without Go on the host, run the same commands in a container:

```sh
docker run --rm --network host -u "$(id -u):$(id -g)" -v "$PWD":/src -w /src \
  -e HOME=/tmp golang:1.25 go test ./libs/go/... ./services/auth/...
```

`internal/http/e2e_integration_test.go` runs the full register → confirm → login → me →
refresh → logout flow and the login throttle against the real database and Redis, with
an in-memory Cognito and locally signed JWTs.

## Configuration

All settings are environment variables; see [`.env.example`](.env.example). Startup fails
and lists every missing or invalid variable at once.

| Variable | Default | Notes |
|----------|---------|-------|
| `PORT` | `8000` | |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `DATABASE_URL` | required | RDS Proxy in AWS, with `sslmode=require` |
| `REDIS_ADDR`, `REDIS_PASSWORD`, `REDIS_TLS` | `127.0.0.1:6379`, empty, `false` | ElastiCache primary endpoint in AWS (cluster mode disabled) |
| `COGNITO_REGION` | `us-east-1` | |
| `COGNITO_USER_POOL_ID`, `COGNITO_CLIENT_ID`, `COGNITO_CLIENT_SECRET` | required | |
| `ORIGIN_VERIFY_ENABLED`, `ORIGIN_VERIFY_SECRET` | `false` | Secret required when enabled |
| `TRUSTED_PROXY_HOPS` | `0` | Proxies that append to `X-Forwarded-For` |
| `LOGIN_MAX_FAILURES_PER_EMAIL`, `LOGIN_MAX_FAILURES_PER_IP`, `LOGIN_FAILURE_WINDOW` | `5`, `20`, `15m` | |
| `PUBLIC_AUTH_MAX_REQUESTS_PER_IP`, `PUBLIC_AUTH_WINDOW` | `30`, `1m` | All public `/auth/*` requests |
| `SHUTDOWN_TIMEOUT` | `20s` | |

## Throttling

- **Failed logins** are counted per email (stored as a SHA-256 hash) and per client IP
  in Redis fixed windows. At the limit, login returns 429 with `Retry-After`, even with
  the right password. A successful login clears the email counter.
- **Public routes** (`register`, `confirm`, `resend-code`, `login`, `refresh`) also have
  a per-IP request limit.
- **Fail open.** If Redis is unreachable, requests are allowed and a warning is logged.
  Throttling is defense in depth behind WAF and API Gateway, and the auth path must not
  depend on Redis being up (Architecture §3.3).

## Known gaps (this is a sketch)

- **Secrets Manager.** Secrets come from environment variables. In ECS they can be
  injected from Secrets Manager by the task definition, with no code change.
- **No tracing.** OpenTelemetry and X-Ray are not wired yet.
- **No circuit breaker** around Cognito. Calls time out after 5 seconds and are retried
  once by the SDK.
- **Client IP behind AWS.** `TRUSTED_PROXY_HOPS` must be confirmed against real traffic
  through CloudFront, API Gateway, and the ALB before the per-IP limits mean anything.
- **ElastiCache.** The Redis client is a single-endpoint client: it works with cluster
  mode disabled, not with cluster mode enabled or Serverless. AUTH token only, no RBAC
  username or IAM auth.
- **Not tested against a real pool yet.** The Cognito behavior is covered by unit tests
  and a fake; run the smoke test and the curl walkthrough once the dev pool exists.
- **Docs drift.** `openapi.yaml` and Interface Design §3.1 say refresh uses
  `InitiateAuth` with `REFRESH_TOKEN_AUTH`. The service uses `GetTokensFromRefreshToken`
  instead: `REFRESH_TOKEN_AUTH` needs a `SECRET_HASH` built from the user's `sub`, which
  the `RefreshRequest` contract does not carry. The endpoint contract is unchanged; only
  the description needs updating.
- **CI** (lint, tests, OpenAPI contract check) is not set up.
