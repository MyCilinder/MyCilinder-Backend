# Functional Requirements

Part of the MyCilinder Software Requirements Specification (SRS).
Related: [Overall Description](./overall-description.md) · [Non-Functional Requirements](./non-functional-requirements.md)

**Conventions**
- Requirement IDs are `FR-<AREA>-<NN>`. "The system shall" is implied in every requirement row.
- **Priority** uses MoSCoW: **M**ust · **S**hould · **C**ould · **W**on't (MVP).
- "Services" refers to the backend microservices: Auth facade, Catalog, Interactions (Go) and Upload/Ingest, Transcoder (Python).

---

## 1. System Features & Operations

### 1.0 Video lifecycle

Many features depend on the status of a video. Statuses are the shared vocabulary between services.

| Status | Meaning | Visible in catalog? | Next states |
|--------|---------|---------------------|-------------|
| `PENDING_UPLOAD` | Record created, waiting for the file to land in S3. | No | `UPLOADED`, expired |
| `UPLOADED` | File received, job queued. | No | `PROCESSING` |
| `PROCESSING` | Validation and transcoding in progress. | No | `READY`, `FAILED` |
| `READY` | Renditions and thumbnail available. | **Yes** | `HIDDEN`, `DELETED` |
| `FAILED` | Validation or transcoding failed after retries. Reason is stored. | No | (retry creates a new attempt) |
| `HIDDEN` | Unpublished by an operator after a report. | No | `READY` |
| `DELETED` | Removed by the owner. | No | terminal |

---

### 1.1 Authentication & Accounts (FR-AUTH)

**Description:** Account lifecycle through the `/auth` facade, which wraps Cognito so clients never depend on Cognito SDK specifics.
**Actors:** Guest, Registered User.

| Input | Processing | Output |
|-------|------------|--------|
| Email, password, display name | Validate, call Cognito sign-up, Cognito sends a verification code | Pending-confirmation account |
| Verification code | Confirm with Cognito | Confirmed account |
| Email + password | Authenticate with Cognito | Access, ID, and refresh tokens (JWT) |
| Refresh token | Exchange with Cognito | New access token |

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-AUTH-01 | A guest can register with email, password, and display name. | M |
| FR-AUTH-02 | An account must be verified by email code before it can log in. | M |
| FR-AUTH-03 | A verified user can log in and receive JWT access and refresh tokens. | M |
| FR-AUTH-04 | A client can exchange a valid refresh token for a new access token. | M |
| FR-AUTH-05 | On first successful login, the system creates a local user profile keyed by the Cognito `sub`. | M |
| FR-AUTH-06 | A user can log out, which revokes the refresh token. | S |
| FR-AUTH-07 | A user can reset a forgotten password with an emailed code. | S |
| FR-AUTH-08 | A user can view their profile and change their display name. | S |
| FR-AUTH-09 | A user can delete their profile and change their profile picture. `[TBD]`| W |

---

### 1.2 Video Upload & Ingest (FR-UP)

**Description:** Creates the video record and hands the client a presigned URL so the file goes directly to S3. The API never handles video bytes.
**Actors:** Creator.

| Input | Processing | Output |
|-------|------------|--------|
| Title, description, file name, content type, file size | Authenticate, check limits and quota (BR-LIM), create record in `PENDING_UPLOAD` | `videoId` and presigned upload URL (or multipart URLs) with short expiry |
| File bytes (client to S3) | S3 stores object in the `raw` bucket and emits `ObjectCreated` | Event on the queue |
| `ObjectCreated` event | Mark `UPLOADED`, enqueue transcode job | Job message |

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-UP-01 | An authenticated user can start an upload by submitting title, description, content type, and size. | M |
| FR-UP-02 | Before issuing a URL, the system validates declared format, size, and the user's daily quota, and rejects violations with a clear error. | M |
| FR-UP-03 | The system creates a video record in `PENDING_UPLOAD` and returns the `videoId` and a presigned URL that expires in a short time (BR-LIM). | M |
| FR-UP-04 | The file is uploaded directly from the client to the `raw` bucket. | M |
| FR-UP-05 | Large files can be uploaded using multipart/resumable upload. | S |
| FR-UP-06 | When the object is created, the system sets the status to `UPLOADED` and enqueues exactly one transcode job per object. | M |
| FR-UP-07 | The system inspects the real file (container, codec, duration, resolution) and rejects files that violate limits, regardless of what the client declared. The video becomes `FAILED` with a reason. | M |
| FR-UP-08 | Records stuck in `PENDING_UPLOAD` for more than 24 h expire, and incomplete multipart uploads are aborted. | S |
| FR-UP-09 | A user can only obtain upload URLs for videos they own. | M |

---

### 1.3 Transcoding & Processing (FR-TR)

**Description:** Asynchronous conversion of the raw upload into HLS renditions.
**Actors:** Transcoder worker (system).

| Input | Processing | Output |
|-------|------------|--------|
| Transcode job (`videoId`, raw object key) | Probe, transcode to HLS 360p/720p, extract thumbnail | HLS segments + playlists + thumbnail in the `output` bucket |
| Job result | Update metadata | Status `READY` with duration and renditions, or `FAILED` with reason |

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-TR-01 | The worker consumes jobs from the queue. Processing must be **idempotent**, because delivery is at-least-once. | M |
| FR-TR-02 | The worker produces HLS renditions at 360p and 720p with a master playlist. It never upscales: a source below 720p yields only the renditions at or below its resolution. | M |
| FR-TR-03 | The worker generates a thumbnail image. | M |
| FR-TR-04 | Outputs are written to the `output` bucket under `videos/{videoId}/`. The `raw` object is never modified or deleted by the pipeline. | M |
| FR-TR-05 | On success, the status becomes `READY`, and duration and available renditions are stored. | M |
| FR-TR-06 | A failed job is retried up to 3 times, then moved to a dead-letter queue. The video becomes `FAILED` with a human-readable reason. | M |
| FR-TR-07 | The status moves to `PROCESSING` when a worker starts a job. | M |
| FR-TR-08 | The owner is notified (email or push) when a video becomes `READY` or `FAILED`. | C |

---

### 1.4 Playback (FR-PB)

**Description:** Watching a video. The API returns metadata and a signed playback URL. The bytes come from CloudFront.
**Actors:** Guest, Registered User.

| Input | Processing | Output |
|-------|------------|--------|
| `videoId` | Load metadata, check status | Video detail (title, description, creator, counts, thumbnail, status) |
| `videoId` | Check access, sign CDN URL/cookie | Signed HLS master playlist URL with expiry |
| View event | Deduplicate (BR-VIEW), increment counter | Updated view count (eventually consistent) |

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-PB-01 | Any viewer can retrieve the detail of a `READY` video. The owner can also retrieve their own non-ready videos. | M |
| FR-PB-02 | The system returns a signed, time-limited playback URL for the HLS master playlist. Unsigned access to media is denied. | M |
| FR-PB-03 | Playback is adaptive (HLS) and served through the CDN. | M |
| FR-PB-04 | The system records a view when playback starts, applying the deduplication rule BR-VIEW. | M |
| FR-PB-05 | Every video has a stable permalink (`/watch/{videoId}`) that opens the video for guests as well. | M |
| FR-PB-06 | Requests for `HIDDEN`, `DELETED`, or unknown videos return "not found" to non-owners. | M |
| FR-PB-07 | Resume-from-last-position and playlists are not supported in the MVP. | W |

---

### 1.5 Browse, Search & Discovery (FR-BR)

**Description:** Finding videos to watch.
**Actors:** Guest, Registered User.

| Input | Processing | Output |
|-------|------------|--------|
| Feed type (recent / most viewed), cursor | Query `READY` videos, sort | Page of video cards + next cursor |
| Search text, cursor | PostgreSQL full-text search on title and description | Ranked page of video cards + next cursor |
| Creator id, cursor | Query creator's `READY` videos | Page of video cards |

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-BR-01 | The system provides a "recent" feed, newest `READY` videos first. | M |
| FR-BR-02 | The system provides a "most viewed" feed (all-time view count). | M |
| FR-BR-03 | A user can search by keyword over title and description. Results are ranked by relevance. | M |
| FR-BR-04 | All lists use cursor-based pagination with a bounded page size (BR-LIM). | M |
| FR-BR-05 | Each list item includes thumbnail, title, creator display name, duration, view count, and publish date. | M |
| FR-BR-06 | Lists only contain `READY` videos. | M |
| FR-BR-07 | A public creator page lists that creator's videos. | S |
| FR-BR-08 | "Most viewed in the last 7 days" feed. | C |
| FR-BR-09 | Personalized recommendations and search suggestions are not supported in the MVP. | W |

---

### 1.6 Interactions (FR-IN)

**Description:** Comments, likes, and counters.
**Actors:** Registered User (write), Guest (read).

| Input | Processing | Output |
|-------|------------|--------|
| `videoId`, comment text | Validate (BR-LIM), store | Created comment |
| `videoId`, like/unlike | Upsert or remove the unique (user, video) like, adjust counter | Current like state and count |
| `videoId`, cursor | Query comments | Page of comments + next cursor |

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-IN-01 | A registered user can post a text comment on a `READY` video. | M |
| FR-IN-02 | Any viewer can list comments for a video, newest first, paginated. | M |
| FR-IN-03 | A user can delete their own comment. | M |
| FR-IN-04 | A video owner can delete any comment on their video. | S |
| FR-IN-05 | A registered user can like a video. Repeating the request has no additional effect (idempotent). | M |
| FR-IN-06 | A registered user can remove their like. | M |
| FR-IN-07 | Video detail shows like count, comment count, and whether the current user has liked it. | M |
| FR-IN-08 | A user can edit their own comment. | C |
| FR-IN-09 | Threaded replies and comment likes are not supported in the MVP. | W |

---

### 1.7 Creator Studio (FR-CR)

**Description:** The creator's view of their own uploads.
**Actors:** Creator.

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-CR-01 | A creator can list their own videos in any status, with status, upload date, and counters. | M |
| FR-CR-02 | For a `FAILED` video, the creator can see the failure reason. | M |
| FR-CR-03 | A creator can edit the title and description of their own video. | S |
| FR-CR-04 | A creator can delete their own video. It disappears from all lists and playback immediately. | S |
| FR-CR-05 | A creator can retry a `FAILED` video without re-uploading, if the raw file is still valid. | C |

---

### 1.8 Reporting & Moderation Hooks (FR-MOD)

**Description:** Minimal hooks. No automated moderation or admin UI in the MVP.
**Actors:** Registered User (reporter), Platform Operator.

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-MOD-01 | A registered user can report a video or a comment with a reason category and optional text. | S |
| FR-MOD-02 | A user cannot file more than one open report for the same target. | S |
| FR-MOD-03 | Reports are stored and can be queried by an operator. | S |
| FR-MOD-04 | An operator can set a video to `HIDDEN` or restore it, through a restricted endpoint or script. | S |
| FR-MOD-05 | Automated content scanning and appeals are not supported in the MVP. | W |

---

## 2. Use Cases

Each use case lists the primary actor, preconditions, main flow, alternate flows, and postconditions.

### UC-01 Register and verify an account
- **Actor:** Guest. **Related:** FR-AUTH-01, 02
- **Preconditions:** The email is not registered.
- **Main flow:**
  1. The guest enters email, password, and display name.
  2. The client sends the registration to the `/auth` facade.
  3. The facade validates input and creates the account in Cognito.
  4. Cognito emails a verification code.
  5. The guest enters the code and the facade confirms it.
- **Alternate flows:**
  - 3a. Email already registered: the system responds with a generic error that does not reveal whether the account exists.
  - 3b. Password violates policy: the system returns which rule failed.
  - 5a. Code expired or wrong: the guest can request a new code.
- **Postcondition:** A confirmed account exists. The local profile is created on first login (FR-AUTH-05).

### UC-02 Log in
- **Actor:** Guest. **Related:** FR-AUTH-03, 04, 05
- **Preconditions:** The account is confirmed.
- **Main flow:**
  1. The user submits email and password.
  2. The facade authenticates against Cognito.
  3. The system returns tokens. On first login it creates the local profile.
- **Alternate flows:**
  - 2a. Invalid credentials: generic error, rate-limited.
  - 2b. Unconfirmed account: the system prompts for verification (UC-01 step 5).
- **Postcondition:** The client holds valid tokens and sends the access token on authenticated calls.

### UC-03 Upload a video
- **Actor:** Creator (system actors: S3, Transcoder worker). **Related:** FR-UP-01–09, FR-TR-01–07
- **Preconditions:** The user is logged in and within the daily quota.
- **Main flow:**
  1. The creator selects a file and enters a title and description.
  2. The client requests an upload from the Upload service.
  3. The service validates the request, creates the record in `PENDING_UPLOAD`, and returns `videoId` and a presigned URL.
  4. The client uploads the file directly to S3.
  5. S3 emits `ObjectCreated`. The system sets `UPLOADED` and enqueues a job.
  6. The worker validates the file, transcodes it, and writes the outputs.
  7. The status becomes `READY`. The video appears in the catalog and in the creator's list.
- **Alternate flows:**
  - 3a. Limit or quota exceeded: the request is rejected with the specific rule broken.
  - 4a. Upload interrupted: the client resumes (multipart) or requests a fresh URL.
  - 4b. URL expired: the client requests a new URL for the same `videoId`.
  - 6a. File invalid (wrong codec, too long, too large): the video becomes `FAILED` with a reason.
  - 6b. Transcode error: retried up to 3 times, then `FAILED` and moved to the dead-letter queue.
- **Postcondition:** The video is `READY` or `FAILED`. The raw file is retained in either case.

### UC-04 Watch a video
- **Actor:** Guest or Registered User. **Related:** FR-PB-01–06
- **Preconditions:** The video is `READY`.
- **Main flow:**
  1. The viewer opens a video from a list, search, or shared link.
  2. The client fetches the video detail and a signed playback URL.
  3. The player streams HLS from the CDN and adapts quality to bandwidth.
  4. The system records a view (deduplicated).
- **Alternate flows:**
  - 1a. Opening a shared permalink works the same way for a guest.
  - 2a. Video is hidden, deleted, or unknown: the client shows "not found".
  - 3a. Signed URL expired mid-session: the client requests a fresh one and continues.
- **Postcondition:** The view count is eventually incremented.

### UC-05 Browse and search
- **Actor:** Guest or Registered User. **Related:** FR-BR-01–07
- **Main flow:**
  1. The user opens the home screen and sees the recent or most-viewed feed.
  2. Optionally, the user enters a search term.
  3. The system returns a page of results with a cursor, and the client loads more on scroll.
- **Alternate flows:** 2a. No results: the client shows an empty state.
- **Postcondition:** None (read-only).

### UC-06 Comment on a video
- **Actor:** Registered User. **Related:** FR-IN-01–04
- **Preconditions:** The user is logged in and the video is `READY`.
- **Main flow:** The user types a comment and submits it. The system validates and stores it, and returns it.
- **Alternate flows:**
  - a. Text empty or too long: the request is rejected.
  - b. Guest tries to comment: the client prompts for login.
  - c. The user or the video owner deletes the comment: it is removed from the list.
- **Postcondition:** The comment is visible to all viewers and the comment count is updated.

### UC-07 Like or unlike a video
- **Actor:** Registered User. **Related:** FR-IN-05–07
- **Main flow:** The user taps like. The system records one like per user and video, and updates the count. Tapping again removes it.
- **Alternate flows:** a. A repeated like request does not change the state (idempotent). b. A guest is prompted to log in.
- **Postcondition:** At most one like exists for the (user, video) pair.

### UC-08 Manage my videos
- **Actor:** Creator. **Related:** FR-CR-01–05
- **Main flow:**
  1. The creator opens "My videos" and sees each video with its status.
  2. The creator edits a title or description, or deletes a video.
- **Alternate flows:** 1a. A `FAILED` video displays its reason, with an optional retry (Could).
- **Postcondition:** Changes are reflected in the catalog. A deleted video is no longer visible or playable.

### UC-09 Report content
- **Actor:** Registered User, then Platform Operator. **Related:** FR-MOD-01–04
- **Main flow:**
  1. The user reports a video or comment and picks a reason.
  2. The system stores the report.
  3. An operator reviews reports and may set the video to `HIDDEN`.
- **Alternate flows:** 2a. A duplicate open report from the same user for the same target is rejected.
- **Postcondition:** The report is recorded. A hidden video disappears from public views.

---

## 3. Business Rules

### 3.1 Policy rules

| ID | Rule |
|----|------|
| BR-01 | Only authenticated, verified users can upload, comment, like, or report. Guests have read-only access. |
| BR-02 | A user can modify or delete only their own resources (videos, comments). Video owners may also delete comments on their videos. Operators are the only role that can hide a video. |
| BR-03 | A video appears in the catalog, search, and playback for non-owners only when its status is `READY`. |
| BR-04 | A user can like a given video at most once. |
| BR-05 | The pipeline never deletes or overwrites raw uploads. User deletion removes the video from the product (soft delete). |
| BR-06 | All media is delivered through signed URLs. Buckets are never public. |
| BR-07 | Like, view, and comment counters are eventually consistent, and a periodic reconciliation job corrects drift against source-of-truth rows. |
| BR-08 | Requests are rate-limited per user and per IP (values in the NFR document). |
| BR-09 | Hidden or deleted content stays hidden even if its permalink is shared. |

### 3.2 View counting rule (BR-VIEW)

A view is counted when playback starts. Repeat views by the same viewer (user id, or an anonymous session id for guests) on the same video within a **30-minute window** are not counted again.

### 3.3 Limits and configurable parameters (BR-LIM)

These are configuration values, not hard-coded. The demo environment may set stricter values to protect credits.

| Parameter | MVP value | Reason |
|-----------|-----------|--------|
| Max video duration | 10 minutes | Bounds transcode cost (R2) |
| Max file size | 1 GB | Bounds storage and upload time |
| Max source resolution | 1080p | Bounds transcode cost |
| Accepted formats | MP4, MOV, WebM | Common, supported by ffmpeg |
| Uploads per user per day | 5 | Abuse and cost control (R10) |
| Title length | 1–100 characters | UI and indexing |
| Description length | 0–5,000 characters | |
| Comment length | 1–1,000 characters | |
| Page size | default 20, max 50 | Protects the read path |
| Presigned upload URL lifetime | 15 minutes | Limits leaked-URL exposure |
| Signed playback URL lifetime | 2 hours | Longer than the longest video |
| Pending-upload expiry | 24 hours | Cleanup |
