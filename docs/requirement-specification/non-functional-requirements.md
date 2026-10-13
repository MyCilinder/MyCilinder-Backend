# Non-Functional Requirements

Part of the MyCilinder Software Requirements Specification (SRS).
Related: [Overall Description](./overall-description.md) · [Functional Requirements](./functional-requirements.md) · [Project charter](../../README.md)

**Conventions**
- IDs are `<AREA>-<NN>`: `PERF`, `SEC`, `REL`, `SCAL`, `MAINT`. The *Charter* column maps each requirement to the high-level objectives NFR1–NFR7 in the README.
- Every requirement has two targets where relevant: the **Design target** (what the architecture must be able to reach, 100k–1M DAU) and the **Demo target** (what we deploy and test within the AWS credit budget). The gap between the two is intentional (README §1, R1).
- Numbers in §5 are estimates built on stated assumptions. They are for sizing and reasoning, not measured results.

---

## 1. Performance

| ID | Requirement | Design target | Demo target | Charter |
|----|-------------|---------------|-------------|---------|
| PERF-01 | API read endpoints (detail, feeds, comments), measured at the edge | p95 < 300 ms, p99 < 800 ms | p95 < 300 ms | NFR3 |
| PERF-02 | API write endpoints (comment, like, upload request) | p95 < 500 ms | p95 < 500 ms | NFR3 |
| PERF-03 | Search endpoint | p95 < 500 ms | p95 < 500 ms | NFR3 |
| PERF-04 | Playback start on a CDN cache hit (first frame) | < 2 s | < 2 s | NFR3 |
| PERF-05 | CDN cache hit ratio for media segments | ≥ 90%  | not measured | NFR3, NFR5 |
| PERF-06 | Time from upload complete to `READY` for a 5-minute video, under normal load | p90 ≤ 10 min (queue wait + transcode) | p90 ≤ 10 min | NFR3 |
| PERF-07 | Throughput the read path sustains at peak | ≈ 2,100 requests/s at 1M DAU (§5) | ≥ 50 requests/s (sized for ≈ 25 concurrent users) | NFR1 |
| PERF-08 | Counter writes (views, likes) must not block the request path | Async/buffered via Redis | Same | NFR1 |
| PERF-09 | Resource footprint: services are right-sized; each Go service runs in ≤ 0.5 vCPU / 1 GB per task | Validate in load test | Same | NFR5 |

**Notes**
- Latency targets apply to API processing, excluding the client's network.
- Media bytes never traverse the API, so API throughput is independent of video bitrate.
- Per-task throughput assumptions (§5.7) must be verified by a load test in the Testing phase.

---

## 2. Security

### 2.1 Requirements

| ID | Requirement | Charter |
|----|-------------|---------|
| SEC-01 | **Authentication:** all mutating endpoints and user-specific reads require a valid Cognito-issued JWT. Tokens are validated at the edge (API Gateway/ALB) **and** re-checked by each service (signature, expiry, audience). | NFR6 |
| SEC-02 | **Authorization:** services enforce ownership checks (BR-02) server-side. Client-supplied user IDs are never trusted; identity comes from the token. | NFR6 |
| SEC-03 | **Transport:** TLS 1.2 or higher on all external traffic. HTTP is redirected or refused. | NFR6 |
| SEC-04 | **Encryption at rest:** S3 buckets, RDS, and Redis use encryption at rest (provider-managed keys are acceptable for the MVP). | NFR6 |
| SEC-05 | **No public buckets:** S3 Block Public Access is on for all buckets. Uploads use short-lived presigned URLs scoped to one object key and size limit. Playback uses signed CloudFront URLs or cookies. Buckets are reachable by CloudFront only through origin access control. | NFR6, R7 |
| SEC-06 | **Least privilege IAM:** each service has its own task role granting only the actions and resources it needs. No wildcard administrator roles at runtime. | NFR6, R7 |
| SEC-07 | **Input validation:** all inputs are validated for type, length, and format. SQL access uses parameterized queries only. Comment and description text is stored raw and escaped on output by clients. | NFR6 |
| SEC-08 | **Untrusted media:** uploaded files are probed with `ffprobe` in an isolated worker (no network access beyond S3/SQS where feasible) before transcoding. File type is determined from content, not from the file name or declared MIME type. | NFR6 |
| SEC-09 | **Rate limiting and quotas:** per-user and per-IP limits at the API edge (starting values: 100 requests/min per user for reads, 20/min for writes, 1 upload/minute). Login endpoints are throttled to resist credential stuffing. | NFR6, R10 |
| SEC-10 | **Secrets:** no secrets in source control or container images. They are stored in AWS Secrets Manager or SSM Parameter Store and injected at runtime. | NFR6 |
| SEC-11 | **Password policy and account protection:** enforced by Cognito (minimum length and complexity, email verification, throttling). No password is ever stored or logged by our services. | NFR6 |
| SEC-12 | **Privacy:** collect only email and display name. Do not log tokens, passwords, or full email addresses. Logs avoid personal data beyond opaque user IDs. | NFR6, C-13 |
| SEC-13 | **CORS:** only the known web origin(s) (Vercel domain, local dev) are allowed. | NFR6 |
| SEC-14 | **Supply chain and IaC scanning in CI:** dependency vulnerability scanning for Go and Python, container image scanning, and `tfsec`/checkov on Terraform. Critical findings block merge. | NFR6, R7 |
| SEC-15 | **Audit trail:** moderation actions (hide/restore) and authentication events are logged with actor, target, and time. | NFR6, NFR7 |
| SEC-16 | **Cost abuse protection:** budget alarms at 50/75/90% of credits, and upload quotas, so abuse cannot burn the budget unnoticed. | NFR5, R1, R10 |

### 2.2 Access control matrix

`R` = read, `C` = create, `U` = update, `D` = delete, `—` = no access, `own` = only own resources.

| Resource | Guest | Registered User | Creator (owner) | Operator |
|----------|-------|-----------------|-----------------|----------|
| Public video list/detail/playback (`READY`) | R | R | R | R |
| Own non-ready videos | — | — | R | R |
| Upload / create video | — | C | C | — |
| Edit / delete video | — | — | U, D (own) | hide/restore any |
| Comments | R | C, D (own) | C, D (own and on own videos) | D (any) |
| Likes | R (counts) | C, D (own) | C, D (own) | — |
| Reports | — | C | C | R, U (resolve) |
| Own profile | — | R, U (own) | R, U (own) | — |
| Metrics, logs, AWS console | — | — | — | R |

---

## 3. Reliability & Availability

### 3.1 Targets

| ID | Requirement | Design target | Demo target | Charter |
|----|-------------|---------------|-------------|---------|
| REL-01 | **Availability of the read/playback path** (browse, detail, playback URL, CDN) | 99.9% monthly ≈ **43.8 min** downtime per month | Best effort | NFR2 |
| REL-02 | Availability of write paths (upload, comment, like) | 99.5% | Best effort | NFR2 |
| REL-03 | Upload and transcoding are asynchronous and may be delayed, but accepted uploads must not be lost. | Zero lost accepted uploads | Same | NFR2, NFR4 |
| REL-04 | **RPO** (maximum data loss) for relational data | ≤ 5 min (RDS point-in-time recovery + Multi-AZ) | ≤ 24 h (daily snapshot) | NFR2, NFR4 |
| REL-05 | **RTO** for loss of one Availability Zone | ≤ 15 min, automatic | Not guaranteed | NFR2 |
| REL-06 | **RTO** for loss of the whole region | ≤ 4 h by re-provisioning with Terraform and restoring data (documented, not exercised in the MVP) | Out of scope | NFR2 |
| REL-07 | Source media durability | S3 durability. Versioning enabled on `raw`. | Same | NFR4, R9 |

### 3.2 Design requirements

| ID | Requirement |
|----|-------------|
| REL-10 | **No single point of failure** in the design: services run as at least 2 tasks across 2 AZs; RDS Multi-AZ; Redis with a replica in another AZ. |
| REL-11 | **Stateless services:** no local session or file state, so any task can be killed and replaced. Health checks (`/healthz` liveness, `/readyz` readiness) drive automatic replacement. |
| REL-12 | **Graceful degradation, by dependency:**<br>• Redis down: serve counts from PostgreSQL (slower, still correct).<br>• Interactions service down: playback and browsing continue.<br>• Transcoder or queue down: uploads are accepted and wait; nothing is lost.<br>• Cognito down: users with valid tokens continue; new logins fail. |
| REL-13 | **Timeouts, retries, and circuit breakers** on every synchronous call. Retries use exponential backoff with jitter and apply only to idempotent operations. |
| REL-14 | **Queue safety:** at-least-once delivery, idempotent consumers, visibility timeout longer than the transcode time, a dead-letter queue after 3 attempts, and an alarm on DLQ depth. |
| REL-15 | **Idempotent writes:** likes and view events are idempotent by design. Upload requests accept a client-supplied idempotency key. |
| REL-16 | **Backups:** automated RDS backups with point-in-time recovery. A restore is tested at least once before the Deployment gate. |
| REL-17 | **Safe deployments:** rolling deployment with health-check gating and automatic rollback on failed health checks. Database migrations are backward compatible with the previous release. |
| REL-18 | **Composite availability:** the read path is a serial chain (CDN, edge, service, cache/DB). Its availability is roughly the product of the parts, so Multi-AZ and caching are used to keep each link above the 99.9% target. Provider SLAs are verified in Design. |

> **Demo deviation (documented, deliberate):** Multi-AZ RDS and redundant Redis roughly double data-tier cost. The demo may run single-AZ with a single task per service to save credits. The design and Terraform modules support the Multi-AZ configuration via variables, and this deviation is called out in the design and test reports.

---

## 4. Scalability & Maintainability

### 4.1 Scalability

| ID | Requirement |
|----|-------------|
| SCAL-01 | **Horizontal scale-out** for all services by adding identical tasks behind the load balancer. No sticky sessions and no instance-local state. |
| SCAL-02 | **Autoscaling policies:** API services scale on CPU and request count per target. Transcoder workers scale on **queue depth** (messages per worker). Minimum and maximum capacities are configurable per environment. |
| SCAL-03 | **Data path off compute:** uploads and playback bypass the API (presigned S3, CloudFront), so media volume does not affect API capacity. |
| SCAL-04 | **Read scaling:** cache hot reads (feeds, video detail) in Redis with short TTLs. Add PostgreSQL read replicas for read-heavy queries before any sharding. |
| SCAL-05 | **Write scaling for counters:** views and likes are aggregated in Redis and flushed to PostgreSQL in batches. Source-of-truth like rows are unique per (user, video). |
| SCAL-06 | **Pagination everywhere:** cursor-based pagination with bounded page size. No unbounded list queries. |
| SCAL-07 | **Growth path** documented in Design, without redesign: partition comments by video, move search to OpenSearch, add a per-region CDN strategy, and move to multi-region later (README §7). |
| SCAL-08 | **Capacity headroom:** provision for ≥ 2× the expected peak. At 1M DAU the design must show no component that saturates below that. |

### 4.2 Maintainability

| ID | Requirement | Charter |
|----|-------------|---------|
| MAINT-01 | **Contract first:** `docs/openapi.yaml` is the single source of truth. Services and the Flutter client are validated against it in CI. | R4, R5 |
| MAINT-02 | **Uniform packaging:** every service is a container built from a Dockerfile, with configuration only through environment variables (12-factor). | R4 |
| MAINT-03 | **CI/CD per service:** each push runs lint, unit tests, and build. Merges to main publish an image to ECR. Infrastructure changes are applied only after `terraform plan` review. | NFR7 |
| MAINT-04 | **Test coverage:** ≥ 70% line coverage on business logic in each service `[TBC]`. | |
| MAINT-05 | **Observability:** structured JSON logs with a correlation ID propagated across services, metrics for request rate/errors/latency (RED), queue depth, and business counters (uploads, transcodes, failures). Alarms on error rate, p95 latency, DLQ depth, and budget. | NFR7 |
| MAINT-06 | **Tracing** of the upload to READY path with X-Ray or equivalent. | NFR7 |
| MAINT-07 | **Documentation:** each service has a README (purpose, run, test, config). Significant design choices are recorded as short decision records in the design description. | |
| MAINT-08 | **Ownership:** each service has a primary and a backup owner (R8). | R8 |
| MAINT-09 | **Independent deployability:** a service can be released without redeploying others. Breaking API changes after the freeze require a change request and versioning (`/v1`). | R5 |
| MAINT-10 | **Style and static checks:** `gofmt`/`golangci-lint` for Go and `ruff`/`black` plus type checking for Python, enforced in CI. | R4 |

---

## 5. Back-of-the-Envelope Calculations

Purpose: show that the architecture is sized with reasoning, identify which resources dominate, and justify the design choices and the demo scale. These are order-of-magnitude estimates. **Every input below is an assumption (`[TBC]`)** and changing one changes the output. Costs are intentionally not computed here: use the AWS Pricing Calculator once the region and services are fixed in Design.

### 5.1 Assumptions

| Input | Value | Notes |
|-------|-------|-------|
| DAU | 100,000 (low) / 1,000,000 (high) | Design range |
| Plays per DAU per day | 5 | |
| Average watch time per play | 4 min | |
| Peak-to-average ratio | 3× | Evening peak |
| Delivered bitrate | 360p ≈ 0.8 Mbps, 720p ≈ 2.5 Mbps; mix 60/40 gives ≈ **1.5 Mbps** average | Includes audio |
| Creators | 1% of DAU upload 1 video/day | |
| Average video | 5 min, source at ~8 Mbps ≈ **300 MB** | |
| API requests per DAU per day | 60 | Feeds, detail, playback URL, comments, likes |
| Interactions per DAU per day | 2 likes, 0.3 comments, 5 view events | |
| Registered users | 5× DAU | |
| CDN cache hit ratio | 90% | Long-tail catalogs are below popular-content ratios |
| Transcode speed | ≈ 1× real time on a 2-vCPU task | Benchmark in Implementation |

### 5.2 Streaming load and egress

Watch-minutes per day = DAU × 5 plays × 4 min.

| Metric | 100k DAU | 1M DAU |
|--------|----------|--------|
| Watch-minutes/day | 2 M | 20 M |
| Average concurrent streams (÷ 1,440 min) | ≈ 1,400 | ≈ 13,900 |
| Peak concurrent streams (×3) | ≈ 4,200 | ≈ 41,700 |
| Average egress bandwidth (× 1.5 Mbps) | ≈ 2.1 Gbps | ≈ 20.8 Gbps |
| Peak egress bandwidth | ≈ 6.3 Gbps | ≈ 62.5 Gbps |
| **Egress per day** | **≈ 22.5 TB** | **≈ 225 TB** |
| Egress per month | ≈ 675 TB | ≈ 6.75 PB |
| CDN-to-origin (S3) traffic at 90% hit | ≈ 2.3 TB/day | ≈ 22.5 TB/day |

*Worked example (1M DAU):* 20 M min × 60 s × 1.5 Mbps = 1.8 × 10⁹ Mb ÷ 8 = 225 TB/day.

### 5.3 API load

| Metric | 100k DAU | 1M DAU |
|--------|----------|--------|
| API requests/day (× 60) | 6 M | 60 M |
| Average requests/s | ≈ 70 | ≈ 700 |
| **Peak requests/s** (×3) | **≈ 210** | **≈ 2,100** |
| Write events/s, average (likes + comments + view events) | ≈ 8 | ≈ 85 |
| Write events/s, peak | ≈ 25 | ≈ 255 |
| Uploads/s, average / peak | 0.01 / 0.03 | 0.12 / 0.35 |

The platform is **read-dominated** (the vast majority of requests are reads) and the write rate is small in absolute terms, so a single PostgreSQL primary with Redis for counters is sufficient.

### 5.4 Storage growth

| Metric | 100k DAU | 1M DAU |
|--------|----------|--------|
| Uploads/day | 1,000 | 10,000 |
| Raw media/day (× 300 MB) | 0.30 TB | 3.0 TB |
| HLS output/day (360p + 720p ≈ 3.3 Mbps × 300 s ≈ 124 MB per video) | 0.12 TB | 1.24 TB |
| **Total media/day** | **≈ 0.42 TB** | **≈ 4.2 TB** |
| Total media/year | ≈ 155 TB | ≈ 1.5 PB |

Relational data per year at 1M DAU: video metadata ≈ 7 GB (10 k × 2 KB × 365) · comments ≈ 55 GB (0.3 M/day × 0.5 KB) · likes ≈ 37 GB (upper bound, 2 M/day × 50 B) · users ≈ 5 GB. Total ≈ **105 GB**, or roughly **200 GB with indexes**. The 100k case is 10× smaller. This fits a single PostgreSQL instance with read replicas; no sharding is needed in year one.

### 5.5 Transcoding compute

| Metric | 100k DAU | 1M DAU |
|--------|----------|--------|
| Video minutes/day (uploads × 5 min) | 5,000 | 50,000 |
| Task-hours/day at 1× real time | ≈ 83 | ≈ 833 |
| Average concurrent 2-vCPU tasks (÷ 24 h) | ≈ 3.5 | ≈ 35 |
| Peak concurrent tasks (×3) | ≈ 10 | ≈ 105 |

Transcoding is **bursty and queue-buffered**. Workers can be provisioned below peak, trading longer wait for lower cost (PERF-06), and scaled on queue depth (SCAL-02).

### 5.6 Availability budget

99.9% monthly availability allows 0.001 × 30.44 days × 24 h × 60 min ≈ **43.8 minutes** of downtime per month (≈ 8.8 h per year). A single-AZ design with manual recovery cannot reliably meet this, which is why REL-10 requires multi-AZ for the read path.

### 5.7 Service sizing (compute)

Assuming one Go API task handles ≈ 500 requests/s `[to be verified by load test]`:
- 1M DAU peak 2,100 req/s ÷ 500 ≈ 5 tasks at full utilization.
- At 50% target utilization for headroom (SCAL-08), and spread across 2 AZs, about **10 API tasks in total** across Catalog, Interactions, and Auth.

### 5.8 Demo-scale sizing (proposed)

| Item | Demo target |
|------|---------------------|
| Concurrent viewers | ≈ 25 |
| Registered users | ≈ 50 |
| Catalog | ≤ 30 curated videos, shorter than the production cap |
| Peak egress | ≈ 25 × 1.5 Mbps ≈ 38 Mbps |
| API load | ≈ 50 requests/s |
| Services | 1 task each; scale-up demonstrated briefly under test |

### 5.9 Conclusions

1. **Egress dominates everything.** At 1M DAU the platform serves about 225 TB per day, versus roughly 100 GB per year of relational data. CDN delivery, bitrate ladder, and cache hit ratio are the design levers that matter most. This is also why the credits cannot sustain real load (R1) and why the demo is throttled.
2. **The API and database are comparatively small** (≈ 2,100 peak requests/s, ≈ 255 writes/s). The right investment is statelessness, caching, and multi-AZ rather than exotic data stores.
3. **Transcoding is the other cost driver** and the one best handled with a queue and capped inputs (R2).
4. **Storage grows about 1.5 PB per year at the top of the range.** Lifecycle rules on `raw` (e.g. transition to a cheaper storage class) become relevant at scale. They are left as Design work, and BR-05 retention must be settled first.
5. **Demo scale is roughly four orders of magnitude below 1M DAU in egress.** The test plan must therefore validate the design by combining small-scale load tests with calculation, and say so explicitly.
