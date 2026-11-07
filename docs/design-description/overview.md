# Design Description: Overview

Part of the MyCilinder Software Design Description (SDD).
Related: [Architecture and Components](./architecture-and-components.md) · [Data Design](./data-design.md) · [Interface Design](./interface-design.md) · [Requirement Specification](../requirement-specification/overall-description.md) · [Project charter](../../README.md)

**Conventions**
- Entry point of the design description. It adds context, decisions, vocabulary, and audience. It does not restate the [README](../../README.md) or the Requirement Specification; it points to them.
- IDs (`FR-*`, `PERF-*`, `SEC-*`, `REL-*`, `SCAL-*`, `MAINT-*`, `BR-*`, `C-*`, `A-*`, `R*`, `O*`) refer to those documents.
- **Design target** is what the architecture must be able to reach (100k–1M DAU). **Demo target** is what is deployed within the AWS credits.

---

## 1. Project Background

MyCilinder is a video-on-demand (VOD) platform with the core experience of a service like YouTube: upload, transcode, browse, watch, and interact (comments, likes). It is the course project for **Sistemas Escalables** (ITESO). It is treated like a real product: the architecture is designed for **100,000 to 1,000,000+ daily active users (DAU)**, while the deployed demo is throttled to fit the **~100–140 AWS credits** available. The design proves the system *can* scale; the demo proves it *works*. Charter objectives O1–O4 are in README §2.1.

| Problem | How the design addresses it |
|---------|-----------------------------|
| Creators need video that plays well on any connection. | Asynchronous transcoding to adaptive-bitrate HLS (360p / 720p). |
| Video is egress-heavy: about 225 TB/day at 1M DAU against about 100 GB/year of relational data (NFR §5.9). | A CDN serves all media; the read path is stateless, cached, and multi-AZ. |
| Large uploads must not load the platform. | Direct-to-S3 presigned uploads; a queue absorbs bursts and workers scale on queue depth. |
| Likes and views create write hotspots. | Counters are buffered in Redis and flushed in batches (eventually consistent). |
| Containers are short-lived and their number changes with load; each task opening its own database connections would exhaust Aurora's connection limit during scale-out and deployments. | Amazon RDS Proxy sits between the services and Aurora and multiplexes task connections onto a bounded pool. |
| A student budget cannot absorb a runaway bill. | Capped inputs, two renditions, Spot workers, quotas, budget alarms (R1, R2). |
| Failures are normal (crashes, Spot reclaims, AZ loss). | No single point of failure on the read path, graceful degradation, idempotent consumers, dead-letter queue (REL-10 to REL-18). |

---

## 2. Scope and Objectives

### 2.1 Document map

| Document | Covers |
|----------|--------|
| Overview (this document) | Background, scope, decisions, glossary, audience. |
| [Architecture and Components](./architecture-and-components.md) | Pattern, diagrams, stack, modules, repository layout, design review findings. |
| [Data Design](./data-design.md) | ERD, schemas, data flow, caching and storage. |
| [Interface Design](./interface-design.md) | APIs, integrations, reference to [`openapi.yaml`](../openapi.yaml). |

The design covers the **backend**. The Terraform and Flutter repos appear only at their boundaries.

### 2.2 Product scope (MVP)

Frozen at the Design gate; later changes need a change request (C-06). The authoritative lists are README §1.1 and the SRS functions PF1–PF8.

- **Included:** accounts through the `/auth` facade, direct-to-S3 upload, asynchronous HLS transcoding, CDN playback, feeds and search, comments / likes / counters, creator studio, reporting hooks with restricted operator endpoints, and a multi-AZ, autoscaled, observable deployment.
- **Excluded:** live streaming, monetization, recommendations and ranked feeds (feeds only sort by most recent or most viewed), DRM, moderation beyond reporting hooks, private / unlisted videos, threaded comments, editing or deleting comments, password reset, retrying a failed video, a full bitrate ladder, multi-region, formal regulatory compliance (C-12), and validation under real production load (A-04).

### 2.3 Design objectives

The design is judged against NFR1–NFR7 (README §2.2) and the detailed targets in the Non-Functional Requirements. Three objectives shape most decisions:
- **Highly available read and playback path** (99.9%, no single point of failure) that does not depend on the application tier once playback access is issued (REL-01, REL-10, README §3.3).
- **Media off compute** (C-08), which keeps API capacity independent of video volume.
- **Contract first**: `openapi.yaml` is the single source of truth and services deploy independently (MAINT-01, MAINT-09).

### 2.4 Design versus demo deployment

| Aspect | Design | Demo |
|--------|--------|------|
| Load | 100k–1M DAU, ≈ 2,100 requests/s peak | ≈ 25 viewers, ≈ 50 requests/s, ≤ 30 short videos (NFR §5.8) |
| Compute | ≥ 2 tasks per service across 2 AZs | 1 task per service |
| Data stores | Aurora writer plus reader replica, Redis primary plus replica, in separate AZs; RDS Proxy in both AZs | Single AZ; RDS Proxy kept, targeting the writer only |
| Availability | 99.9% read path | Best effort |

Terraform variables switch between the two. HA behavior the demo does not exercise (such as AZ failover) is validated by review or a one-off test, and the test plan says so.

### 2.5 Design decisions

| # | Decision | Outcome | Source / rationale |
|---|----------|---------|--------------------|
| P1 | Region | **`us-east-1`** | Team decision. Broad service coverage and quotas; CloudFront-attached resources (WAF web ACLs, ACM certificates) must live there. Playback latency does not depend on it because media comes from the CDN. |
| P2 | Transcoding | **ffmpeg on Fargate Spot** | README §3.1, §3.2. |
| P3 | Relational database | **Aurora PostgreSQL Serverless v2** | README §3.2. The account is on the AWS Free plan with credits, which offers only the serverless flavor, up to 4 ACU and 1 GiB per cluster. |
| P4 | Cache and counters | **ElastiCache Redis** | README §3.2. |
| P5 | Auth facade | **Go** | README §3.2. |
| P6 | Environments | Local (Docker Compose), AWS development, AWS demo | Overall Description §3. All AWS environments share the same credits. |
| P7 | Operator actions | **Restricted endpoints** under `/ops/*`, for members of the Cognito `operators` group | Team decision; specified in the Interface Design §1.2. |
| P8 | Public domain | **Default CloudFront domain** (`*.cloudfront.net`), no custom domain | Team decision: cheapest option. It needs no domain registration, Route 53 hosted zone, or certificate, and CloudFront provides TLS for it. |
| P9 | API entry point | **API served through CloudFront** (`/api/*`) with WAF attached to the distribution | Design review F1 (Architecture §5). It also puts API and media on one domain for signed cookies (F2). |
| P10 | Storage lifecycle | **Raw uploads move to S3 Glacier Flexible Retrieval after 30 days; HLS outputs of deleted videos are removed after 7 days** | Team decision (NFR §5.9). Raw uploads are still never deleted (BR-05). Details in the Data Design §3.3. |
| P11 | Database connection management | **Amazon RDS Proxy** between ECS and Aurora; services never connect to Aurora directly | README §3.2. Fargate tasks are ephemeral and autoscaled, so per-task connections would grow with the task count and spike during deployments. The proxy keeps Aurora's connection count bounded and keeps client connections open during failover (Architecture §4.3, SCAL-09). |

---

## 3. Glossary and Definitions

User classes (Guest, Registered User, Creator, Platform Operator) are defined in the SRS Overall Description §2, and the video statuses in Functional Requirements §1.0. AWS service roles are described in README §3.2.

| Term | Definition |
|------|------------|
| **VOD** | Video on demand: pre-recorded video watched whenever the viewer chooses. |
| **DAU** | Daily active users. Design range: 100,000 to 1,000,000. |
| **Transcoding** | Converting a source video into other resolutions and bitrates. |
| **HLS** | HTTP Live Streaming: video split into short segments listed in `.m3u8` playlists, delivered over HTTP. |
| **ABR / rendition** | Adaptive bitrate (the player switches quality with bandwidth) / one encoded version of a video (360p or 720p). |
| **Egress** | Data leaving the cloud toward users. It dominates cost and load at scale. |
| **ffmpeg / ffprobe** | Open-source tools to transcode video and to inspect a file's real format, duration, and resolution. |
| **Presigned URL** | Time-limited URL that lets a client upload or download one S3 object without AWS credentials. |
| **OAC** | Origin Access Control: lets CloudFront read a private S3 bucket while the bucket stays closed to everyone else. |
| **Signed URL / cookie** | Time-limited CloudFront authorization for private content. Unsigned media access is denied. |
| **VPC Link** | Private connection from API Gateway into the VPC, ending at the internal ALB. |
| **Fargate Spot** | Serverless containers at a discount, but interruptible. Used for the transcoder. |
| **JWT / Auth facade** | Signed identity token issued by Cognito / our `/auth` service that wraps Cognito so clients never depend on it (R6). |
| **Stateless service** | Keeps no local session or file state, so any instance can be replaced or added. |
| **Availability Zone (AZ)** | Isolated data-center group inside a region. Using two tolerates the loss of one. |
| **SPOF** | Single point of failure: a component whose failure stops the system. |
| **SLO** | Service level objective: internal reliability target, for example 99.9% per month. |
| **RPO / RTO** | Maximum acceptable data loss / time to restore service, both measured in time. |
| **Failover** | Automatic switch to a standby (for example a database replica) when the primary fails. |
| **Connection exhaustion** | The database refuses new connections because the clients together have opened more than its `max_connections` limit. |
| **RDS Proxy / connection pooling** | Managed AWS proxy that keeps a shared pool of database connections and lends one to a client for each transaction, so many clients share few database connections. |
| **Pinning** | RDS Proxy dedicating one database connection to one client for the whole session, because the session changed state (for example with `SET`). It reduces pooling and is avoided by design (Architecture §3). |
| **Graceful degradation** | Losing a non-critical dependency reduces features or speed instead of causing a full outage. |
| **Idempotency** | Repeating an operation has the same effect as doing it once. Needed because queue delivery is at-least-once. |
| **Eventual consistency** | Reads may briefly lag writes. Counters can lag by a few seconds (A-10). |
| **DLQ** | Dead-letter queue: holds messages that failed repeatedly, so they can be inspected. |
| **p95 / p99** | Latency under which 95% / 99% of requests complete. |
| **Back-of-the-envelope** | Order-of-magnitude sizing from stated assumptions (NFR §5). |
| **Walking skeleton** | Thin end-to-end implementation built first to expose integration problems (R5). |
| **C4 model** | Diagramming at four levels: context, containers, components, code. |

---

## 4. Target Audience and Stakeholders

Readers: the development team, the course professor (gate review), future maintainers, and the frontend developers who depend on the API contract. Suggested order: Overview → Architecture → Data → Interface → `openapi.yaml`.

| Stakeholder | Who | Interest / involvement |
|-------------|-----|------------------------|
| **Development team** | Diego Gomez, Dario Lucciano, Alexis Flores | Design, build, test, deploy, and operate. The team reviews and signs off each phase gate. |
| **Course professor** | Professor of Sistemas Escalables, ITESO | Evaluates scalability reasoning, documentation, and process. |
| **Viewers and creators** | Guests, registered users, uploaders | Use the Flutter client; permissions are in SRS §2 and NFR §2.2. |
| **Platform operators** | Team members | Run dashboards and alarms; hide content through restricted endpoints. |
| **Frontend and Infra repos** | `MyCilinder-Frontend`, `MyCilinder-Infra` | The frontend consumes `openapi.yaml`; Infra provisions what the backend needs. |
| **Providers** | AWS, Vercel (optional), GitHub | Their quotas and plan limits constrain the design. |

### Ownership

There is no separate QA, security, or operations staff, so each concern is a **role** held by a team member. Assignments were made arbitrarily and can change. They satisfy R8 and MAINT-08.

| Role | Primary | Backup |
|------|---------|--------|
| Architect and documentation | Alexis Flores | Diego Gomez |
| Go services (Auth, Catalog, Interactions) | Diego Gomez | Dario Lucciano |
| Python pipeline (Upload / Ingest, Transcoder) | Dario Lucciano | Alexis Flores |
| Infrastructure (Terraform, CI/CD) | Alexis Flores | Dario Lucciano |
| Frontend | Dario Lucciano | Diego Gomez |
| Testing | Diego Gomez | Alexis Flores |
| Cost and security watch | Alexis Flores | Dario Lucciano |
