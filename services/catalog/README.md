# Catalog / Metadata API

Go service for browsing and watching videos: feeds, search, creator pages, video detail,
signed playback, the creator studio, and operator moderation of videos
([`docs/openapi.yaml`](../../docs/openapi.yaml)). It owns every status change of a video
after Upload creates it, through the `video-status-queue` consumer and three background
jobs.

Design references: Architecture §2.3 and §4.2, Interface Design §1.2, §1.3 and §2.1, Data
Design §1.3, §2 and §3, and the component diagram
[`c4-level3-catalog`](../../docs/diagrams/c4-level3-catalog.puml). The implementation plan
is [`CATALOG_SERVICE_PLAN.md`](../../CATALOG_SERVICE_PLAN.md).

## Endpoints

All under `/api/v1`.

| Route | Auth | Success | Notes |
|-------|------|---------|-------|
| `GET /feed?sort=recent\|most_viewed` | Public | 200 | `READY` only |
| `GET /search?q=` | Public | 200 | Spanish full-text, `q` 1–200 characters, plain words |
| `GET /creators/{userId}/videos` | Public | 200 | 404 if the user has no profile |
| `GET /videos/{videoId}` | Optional | 200 | Owner also sees non-`READY` videos, with `status` and `failureReason` |
| `GET /videos/{videoId}/playback` | Optional | 200 | Three `Set-Cookie` lines, `Cache-Control: no-store` |
| `GET /me/videos` | JWT | 200 | Every status except `DELETED`, with like and comment counts |
| `PATCH /videos/{videoId}` | Owner | 200 | `title` and/or `description`; unknown fields rejected |
| `DELETE /videos/{videoId}` | Owner | 204 | Soft delete |
| `POST /ops/videos/{videoId}/hide` | Operator | 204 | `READY` → `HIDDEN`, audited |
| `POST /ops/videos/{videoId}/restore` | Operator | 204 | `HIDDEN` → `READY`, audited |
| `GET /healthz`, `GET /readyz` | none | 200 / 503 | `/readyz` checks the database only |

Lists take `cursor` and `limit` (1–50, default 20) and return `{items, nextCursor}`.
Cursors are keyset positions; no query uses `OFFSET`.

### Behavior worth knowing

- **Hidden means not found.** A video that is not `READY` returns 404 to everyone but its
  owner, so its existence is never revealed. A malformed id is also 404.
- **Owner-only fields.** `status` and `failureReason` are returned only to the owner, and
  owner responses carry `Cache-Control: private, no-store`. Media fields (`thumbnailUrl`,
  `durationSeconds`, `renditions`, `publishedAt`) stay empty until the video is `READY`.
- **Playback.** A non-`READY` video returns 409 to its owner and 404 to everyone else.
  Without `CLOUDFRONT_*` the endpoint returns 503.
- **Edits and deletes by someone else** return 403 if the video is `READY` (anyone can see
  it) and 404 otherwise. Deleting a `PENDING_UPLOAD`, `PROCESSING` or `EXPIRED` video
  returns 409; deleting a deleted one returns 404 (plan decisions 3 and 4).
- **Moderation.** Hiding a video that is already `HIDDEN`, or restoring one that is
  already `READY`, returns 409. The status change and the `audit.audit_log` row
  (`video.hidden` / `video.restored`) commit in one transaction.
- **Status events arrive out of order.** `video.ready` and `video.failed` are also accepted
  from `PENDING_UPLOAD`, because SQS does not keep order (plan decision 1). Duplicates and
  late events are acknowledged and change nothing. `EXPIRED` and `DELETED` are final.
- **Search** treats the query as plain words (`plainto_tsquery('spanish', …)`), so `&`,
  `|`, `!` and `OR` are not operators. Every word must match. Title matches rank above
  description matches. The query is trimmed and lower-cased first.
- **Caching** (Data Design §3.2). Feed and search pages are cached for 30 s and expire
  only; a hidden or deleted video can stay in a cached page for up to 30 s. `READY` video
  detail is cached for 60 s and deleted on every write. If Redis is down, every read goes
  to Postgres and a warning is logged.
- **Reads** use `DATABASE_READER_URL` and retry once on the writer if the reader cannot be
  reached. `/me/videos` and every write use the writer.
- **Creators without a profile** are shown as `Usuario`.
- **Thumbnails** are unsigned CloudFront URLs. Without `CLOUDFRONT_DOMAIN` they are
  relative paths (`/thumbnails/{id}.jpg`).
- **Output cleanup** sets `catalog.videos.outputs_purged_at`, so a purged video is not
  listed in S3 again (plan decision 2). Outputs of `HIDDEN` videos are kept, so a restore
  needs no reprocessing.

## Layout

```
cmd/catalog/          service entry point (also `catalog healthcheck` for container probes)
cmd/send-event/       dev tool: publish a video.* status event, standing in for the Transcoder
scripts/seed-media.sh dev tool: upload test HLS media for seeded videos to the dev bucket
internal/video/       domain types, title/description rules, visibility, video detail
internal/browse/      feed, search, creator pages
internal/studio/      /me/videos, edit, soft delete
internal/moderation/  operator hide and restore, with the audit row
internal/lifecycle/   transition table and the conditional status UPDATE
internal/playback/    playback visibility rules
internal/cfsign/      CloudFront signed cookies (PKCS #1 or PKCS #8 keys)
internal/events/      video-status-queue consumer
internal/jobs/        expiry, stuck-processing sweep, output cleanup (advisory locks)
internal/storage/     hls-output cleaner (S3)
internal/cache/       Redis cache-aside with single-flight
internal/cursor/      opaque keyset cursors
internal/store/       catalog.videos repository and audit writer (pgx)
internal/http/        router, handlers, request decoding, problem mapping
internal/config/      environment configuration
internal/testdb/      integration test fixtures
```

The shared pieces (config loading, Problem Details, middleware, JWT validation, database
and Redis clients, health handlers) are in [`libs/go`](../../libs/go).

## AWS dev resources

Catalog uses the development stack through the standard AWS credential chain:
`AWS_PROFILE` locally, the task role on ECS.

| Resource | Used by | Setting |
|----------|---------|---------|
| SQS `video-status-queue` (with its DLQ) | Status consumer | `VIDEO_STATUS_QUEUE_URL` |
| SNS `video-events` | `send-event` only | `VIDEO_EVENTS_TOPIC_ARN` |
| S3 `hls-output` | Cleanup job; `seed-media.sh` | `HLS_OUTPUT_BUCKET` |
| CloudFront distribution and trusted key group | Signed playback | `CLOUDFRONT_DOMAIN`, `CLOUDFRONT_KEY_PAIR_ID`, `CLOUDFRONT_PRIVATE_KEY_FILE` |
| Cognito user pool and app client | Token validation | `COGNITO_USER_POOL_ID`, `COGNITO_CLIENT_ID` |

IAM permissions:

| Permission | Resource | Needed by |
|------------|----------|-----------|
| `sqs:ReceiveMessage`, `sqs:DeleteMessage` | `video-status-queue` | Service |
| `s3:ListBucket` | `hls-output` bucket | Service (cleanup job) |
| `s3:DeleteObject` | `hls-output/*` | Service (cleanup job) |
| `sns:Publish` | `video-events` | `send-event` (dev only) |
| `sqs:SendMessage` | `video-status-queue` | `send-event --sqs` (dev only) |
| `s3:PutObject` | `hls-output/*` | `seed-media.sh` (dev only) |

The CloudFront private key is a PEM file. Put it at `secrets/cloudfront.pem` (gitignored;
see [`secrets/README.md`](secrets/README.md)). PKCS #1 (`BEGIN RSA PRIVATE KEY`) and
PKCS #8 (`BEGIN PRIVATE KEY`) are both accepted. On ECS the key will come from Secrets
Manager.

The `video-events` → `video-status-queue` subscription should use raw message delivery
(Interface Design §2.1). The consumer also accepts the SNS-wrapped form, so it keeps
working if that setting is off.

## Running locally

From the repository root. Copy `.env.example` to `.env` and fill it in first.

```sh
# Postgres, Redis, roles, migrations, seed data, and the services in containers. Clients
# use the gateway on port 8000; catalog is also published directly on port 8001.
docker compose -f deploy/local/docker-compose.yml up -d --build

# or: only the dependencies, then run the service from source. With `go run`, point
# CLOUDFRONT_PRIVATE_KEY_FILE at the host path (services/catalog/secrets/cloudfront.pem).
docker compose -f deploy/local/docker-compose.yml up -d postgres redis seed
set -a; . services/catalog/.env; set +a
CLOUDFRONT_PRIVATE_KEY_FILE=services/catalog/secrets/cloudfront.pem \
  go run ./services/catalog/cmd/catalog
```

The Compose `catalog` service runs as your host user (`LOCAL_UID`/`LOCAL_GID`, default
1000) and mounts `~/.aws` read-only at `/aws`, so the profile in `AWS_PROFILE` works inside
the container.

### Seed data

The `seed` one-shot loads [`deploy/local/seed/seed.sql`](../../deploy/local/seed/seed.sql)
after every migration, on every `up`. It is idempotent: existing rows are left alone, so
videos changed by the jobs or the walkthrough keep their new state. To start over:

```sh
docker compose -f deploy/local/docker-compose.yml exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U postgres -d mycilinder < deploy/local/seed/reset.sql
docker compose -f deploy/local/docker-compose.yml up seed
```

It creates 6 users, 30 videos in every status with Spanish titles, counters, likes, and
comments. Ids follow a fixed pattern, `5eed1000-0000-4000-8000-0000000000NN`:

| `NN` | Status | Used for |
|------|--------|----------|
| 01–14 | `READY` | Feed, search, detail. 01 and 02 get real media from `seed-media.sh` |
| 15 | `HIDDEN` | Moderation |
| 16, 17 | `DELETED` 8 days ago | Output cleanup |
| 18 | `PENDING_UPLOAD`, 30 h old | Expiry job |
| 19 | `PROCESSING`, 3 h old | Stuck-processing sweep |
| 20 | `FAILED` | Owner view with `failureReason` |
| 21–28 | Mixed | Owned by your test user (`SEED_OWNER_SUB`) |
| 29 | `EXPIRED` | Final status |
| 30 | `PENDING_UPLOAD`, fresh | Status-event demo |

Set `SEED_OWNER_SUB` in `.env` to your Cognito test user's `sub` to own videos 21–28. If it
is empty they belong to a placeholder user; setting it later and running `up seed` again
moves them to you.

Upload test media for videos 01 and 02 (uses `ffmpeg`, or Docker if `ffmpeg` is not
installed):

```sh
services/catalog/scripts/seed-media.sh
```

### Walkthrough with curl

```sh
B=http://127.0.0.1:8000/api/v1
V=5eed1000-0000-4000-8000-000000000001

# Browse
curl -s "$B/feed?limit=5" | jq '.items[].title, .nextCursor'
curl -s "$B/feed?limit=5&cursor=$(curl -s "$B/feed?limit=5" | jq -r .nextCursor)" | jq '.items[].title'
curl -s "$B/feed?sort=most_viewed&limit=3" | jq '.items[] | {title, viewCount}'
curl -s "$B/search?q=recorridos%20campus" | jq '.items[].title'
curl -s "$B/creators/5eed0000-0000-4000-8000-000000000005/videos" | jq '.items[].title'

# Detail: READY for a guest; FAILED is 404 for a guest
curl -s "$B/videos/$V" | jq
curl -s -o /dev/null -w '%{http_code}\n' "$B/videos/5eed1000-0000-4000-8000-000000000020"

# Signed playback
curl -s -D - "$B/videos/$V/playback" -o /tmp/pb.json | grep -i set-cookie
COOKIE=$(jq -r '.cookies | to_entries | map("\(.key)=\(.value)") | join("; ")' /tmp/pb.json)
curl -s -H "Cookie: $COOKIE" "$(jq -r .masterPlaylistUrl /tmp/pb.json)"
```

The owner and operator routes need an access token from the auth facade
(`POST /auth/login`; see [`../auth/README.md`](../auth/README.md)). The operator must be in
the Cognito `operators` group.

```sh
AT=…   # your test user (SEED_OWNER_SUB)
OP=…   # a user in the operators group

curl -s "$B/me/videos" -H "Authorization: Bearer $AT" | jq '.items[] | {title, status, failureReason}'
curl -s "$B/videos/5eed1000-0000-4000-8000-000000000026" -H "Authorization: Bearer $AT" | jq '{status, failureReason}'
curl -s -X PATCH "$B/videos/5eed1000-0000-4000-8000-000000000021" -H "Authorization: Bearer $AT" \
  -d '{"title":"Mi primer video (editado)"}' | jq .title
curl -i -X DELETE "$B/videos/5eed1000-0000-4000-8000-000000000023" -H "Authorization: Bearer $AT"   # 204
curl -s -o /dev/null -w '%{http_code}\n' "$B/videos/5eed1000-0000-4000-8000-000000000023"            # 404

curl -i -X POST "$B/ops/videos/$V/hide" -H "Authorization: Bearer $OP"       # 204
curl -s -o /dev/null -w '%{http_code}\n' "$B/videos/$V"                     # 404 for guests
curl -i -X POST "$B/ops/videos/$V/restore" -H "Authorization: Bearer $OP"    # 204
docker compose -f deploy/local/docker-compose.yml exec postgres \
  psql -U postgres -d mycilinder -c "select action, actor_id, target_id, details from audit.audit_log where service='catalog'"
```

### Status events

With `CONSUMER_ENABLED=true` the service long-polls `video-status-queue`. `send-event`
publishes to the `video-events` topic, the same path the Transcoder uses:

```sh
set -a; . services/catalog/.env; set +a
N=5eed1000-0000-4000-8000-000000000030
go run ./services/catalog/cmd/send-event --type video.processing --video $N
go run ./services/catalog/cmd/send-event --type video.ready --video $N --duration 30 --renditions 360p,720p
curl -s "$B/feed?limit=1" | jq '.items[0].id'     # the new video is first
go run ./services/catalog/cmd/send-event --type video.ready --video $N --duration 99   # duplicate: ignored
go run ./services/catalog/cmd/send-event --type video.failed --video <id> --reason "unsupported codec"
go run ./services/catalog/cmd/send-event --sqs --raw '{not json'   # left on the queue, then the DLQ
```

| Message | Result |
|---------|--------|
| Moves the video forward | Applied, message deleted |
| Duplicate, late, or for a final status | Ignored, message deleted |
| Unknown video id | Warning logged, message deleted |
| Unparseable or invalid | Left on the queue; reaches the DLQ after the redrive limit |
| Database error | Left on the queue for redelivery |

### Background jobs

With `JOBS_ENABLED=true` the service runs the expiry job, the stuck-processing sweep, and
the output cleanup. Each run takes `pg_try_advisory_xact_lock`, so several tasks can run
them and only one does the work. For a quick demo, set short intervals:

```sh
JOBS_ENABLED=true JOB_INTERVAL=10s CLEANUP_INTERVAL=10s \
  docker compose -f deploy/local/docker-compose.yml up -d catalog
# within ~15 s: video 18 becomes EXPIRED, 19 becomes FAILED ("processing timed out"),
# and the outputs of 16 and 17 are removed (outputs_purged_at is set).
```

**The cleanup job really deletes objects from `HLS_OUTPUT_BUCKET`.** Point it only at the
dev bucket. To watch it remove real files: delete video 02 as its owner (or set it to
`DELETED` with `deleted_at` 8 days ago in SQL), then check
`aws s3 ls s3://$HLS_OUTPUT_BUCKET/videos/5eed1000-0000-4000-8000-000000000002/`.

## Tests

```sh
# unit tests (no dependencies)
go test ./services/catalog/...

# integration tests: Postgres and Redis from Compose. Fixtures use random ids and are
# deleted afterwards, so the seed data is not touched.
TEST_DATABASE_URL='postgres://catalog_svc:catalog_svc_local@127.0.0.1:5432/mycilinder?sslmode=disable' \
TEST_ADMIN_DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5432/mycilinder?sslmode=disable' \
TEST_REDIS_ADDR=127.0.0.1:6379 \
  go test -race -tags integration ./services/catalog/...

# grant checks for every service role
docker compose -f deploy/local/docker-compose.yml exec -T postgres \
  psql -v ON_ERROR_STOP=1 -U postgres -d mycilinder < deploy/local/checks/grants.sql
```

The integration tests connect as `catalog_svc`, so they also exercise its grants. AWS is
faked in tests: the consumer, the S3 cleaner, and the cookie signer run against in-memory
fakes or generated keys.

## Configuration

All settings are environment variables; see [`.env.example`](.env.example). Startup fails
and lists every missing or invalid variable at once.

| Variable | Default | Notes |
|----------|---------|-------|
| `PORT` | `8001` | |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `DATABASE_URL` | required | RDS Proxy read/write endpoint in AWS, `sslmode=require` |
| `DATABASE_READER_URL` | `DATABASE_URL` | RDS Proxy read-only endpoint |
| `REDIS_ADDR`, `REDIS_PASSWORD`, `REDIS_TLS` | `127.0.0.1:6379`, empty, `false` | |
| `COGNITO_REGION`, `COGNITO_USER_POOL_ID`, `COGNITO_CLIENT_ID` | `us-east-1`, required, required | No client secret |
| `OPERATORS_GROUP` | `operators` | `cognito:groups` value for `/ops` |
| `ORIGIN_VERIFY_ENABLED`, `ORIGIN_VERIFY_SECRET` | `false` | Secret required when enabled |
| `AWS_REGION` | `us-east-1` | SQS and S3 |
| `AWS_PROFILE` | unset | Local only; read by the AWS SDK |
| `CONSUMER_ENABLED`, `VIDEO_STATUS_QUEUE_URL` | `false` | URL required when enabled |
| `JOBS_ENABLED`, `HLS_OUTPUT_BUCKET` | `false` | Bucket required when enabled |
| `JOB_INTERVAL` | `15m` | Expiry and sweep |
| `PENDING_UPLOAD_EXPIRY`, `PROCESSING_TIMEOUT` | `24h`, `2h` | |
| `CLEANUP_INTERVAL`, `OUTPUT_RETENTION` | `24h`, `168h` | |
| `CLOUDFRONT_DOMAIN`, `CLOUDFRONT_KEY_PAIR_ID`, `CLOUDFRONT_PRIVATE_KEY_FILE` | unset | All three or none |
| `SHUTDOWN_TIMEOUT` | `20s` | |
| `VIDEO_EVENTS_TOPIC_ARN` | unset | `send-event` only |
| `SEED_OWNER_SUB` | unset | Compose `seed` only |

## Known gaps (this is a sketch)

- **Secrets Manager.** The CloudFront key is read from a file. On ECS it can be mounted from
  Secrets Manager, or the loader can read the secret directly.
- **No tracing.** OpenTelemetry and X-Ray are not wired yet.
- **No per-user rate limit** and **no circuit breaker**. WAF and API Gateway throttling
  still apply in AWS.
- **RDS Proxy borrow timeouts** are reported as 503 like any other database error, without
  a specific mapping.
- **No CORS.** The local gateway adds dev-only CORS; in AWS the web client is same-origin
  through CloudFront.
- **No ECS or Terraform wiring.**
- **Feed staleness.** Feed and search pages can show a hidden or deleted video for up to
  30 s (expiry-only keys, Data Design §3.2).
