# Mock API (frontend demos only)

An in-memory stand-in for the **Catalog, Interactions and Upload** services. It lets the
Flutter client run every screen before those services are built. **Auth is real:** the client
signs in through the auth facade (Cognito), and the mock verifies the same access tokens
against the same user pool.

Development only. It is never built by CI or deployed.

## Run

From the repo root, with `services/auth/.env` filled in (the mock reads the Cognito values
from it):

```sh
docker compose -f deploy/local/docker-compose.yml -f deploy/local/docker-compose.mock.yml up -d --build
```

The client keeps the same base URL: `http://localhost:8000/api/v1` (Android emulator:
`http://10.0.2.2:8000/api/v1`). To go back to the real services, run `up -d` again without
the second `-f` (the gateway is recreated with the real upstreams).

All state is in memory and resets when the container restarts:
`docker compose -f deploy/local/docker-compose.yml -f deploy/local/docker-compose.mock.yml restart mock-api`.

## How it switches

`gateway/nginx.conf` is an nginx-image template. `docker-compose.yml` sets
`CATALOG_UPSTREAM`, `INTERACTIONS_UPSTREAM` and `UPLOAD_UPSTREAM` to the real services, and
`docker-compose.mock.yml` points all three at `mock-api`. `/auth/*` and `/me` always go to
the auth facade. The gateway also routes `/media/*` and `/mock-s3/*` to the mock. Without
the mock running, those two paths return the 503 problem.

## What it does

Every route in `docs/openapi.yaml` outside `/auth` and `/me` is implemented, with the
contract's status codes, problem bodies, and visibility rules (owner versus guest, `READY`
only in lists, 403 or 404 for non-owners, 409 on wrong statuses).

| Area | Behaviour |
|------|-----------|
| Seed data | 5 creators and 18 `READY` videos with Spanish titles, view and like counts, and a few comments. One extra video is `HIDDEN`. |
| Your own videos | The first time a signed-in user calls any route, they get 5 videos of their own: 2 `READY`, 1 `PROCESSING`, 1 `FAILED` (with a reason), and 1 `HIDDEN`. This gives the studio every state to show. Disable with `MOCK_SEED_PER_USER=false`. |
| Display names | Read from the real `GET /api/v1/me` on the auth facade, so comments and studio show the user's real name. |
| Playback | `masterPlaylistUrl` is a plain **720p MP4** (`/media/videos/{id}/video.mp4`), not an HLS playlist. Seeking works, because Range requests are supported. The three `CloudFront-*` cookies and the `cookies` map have the contract's shape, but the values are fake and nothing checks them. Each video maps to one of 4 stock clips of 5 to 10 s. `durationSeconds` matches the clip. |
| Thumbnails | `/media/thumbnails/{id}.jpg`, one of 12 placeholder images. |
| Search | Case-insensitive and accent-insensitive. Every word must match, plurals are stripped roughly (`recorridos` matches `recorrido`), and title matches rank first. This only approximates Postgres' Spanish full-text search. |
| Pagination | `limit` 1–50 and opaque `nextCursor` strings, as in the contract. The cursors hold offsets, not keysets. |
| Likes, comments, views, reports | Stored and reflected in `/stats` and `/me/videos`. Views are deduplicated per user, or per `X-Session-Id` for guests, for 30 minutes. |
| Moderation | `/ops/*` requires the `operators` group in the real token. Hide, restore, report resolution, and comment hiding all work. |
| Upload | `POST /uploads` validates the body and `Idempotency-Key`, and returns a single `PUT` (≤ 100 MB) or `MULTIPART` instructions whose URLs point at `/mock-s3/...` on the gateway. The uploaded bytes are **read and discarded**. After the PUT, or after `/complete` for multipart, the video is `PROCESSING`. After `MOCK_PROCESSING_DELAY` (default `15s`) it becomes `READY` and plays a stock clip, not your file. Set the delay to `0s` to leave it in `PROCESSING`. There is no daily quota. |

URLs in responses are built from the host the client used (`X-Forwarded-Host` from the
gateway), so they work for both `localhost:8000` and `10.0.2.2:8000`. Set `MEDIA_BASE_URL`
on `mock-api` to override this, for example for a phone on your LAN.

## Settings

| Variable | Default | Meaning |
|----------|---------|---------|
| `MOCK_PROCESSING_DELAY` | `15s` | Upload `PROCESSING` → `READY` delay; `0s` never finishes. |
| `MOCK_SEED_PER_USER` | `true` | Starter videos for each signed-in user. |
| `MOCK_API_PORT` | `8090` | Host port for direct access (bypassing the gateway). |
| `MEDIA_BASE_URL` | (from request) | Base for media and upload URLs. |

## Quick check

```sh
G=http://localhost:8000/api/v1
curl -s "$G/feed?limit=3" | jq '.items[] | {title, viewCount}'
curl -s "$G/search?q=recorridos%20campus" | jq '.items[].title'
TOKEN=$(curl -s -X POST "$G/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"<you>","password":"<password>"}' | jq -r .accessToken)
curl -s "$G/me/videos" -H "Authorization: Bearer $TOKEN" | jq '.items[] | {title, status}'
```

## Tests

```sh
cd deploy/local/mock-api && go test ./...
```

The tests use a fake token verifier and cover browse, watch, interactions, studio,
moderation, and upload.

Media credits: Big Buck Bunny and Sintel (Blender Foundation, CC BY 3.0) via
test-videos.co.uk; Jellyfish (test-videos.co.uk); Flower (MDN, CC0); thumbnails from
picsum.photos. The files are downloaded when the image is built.
