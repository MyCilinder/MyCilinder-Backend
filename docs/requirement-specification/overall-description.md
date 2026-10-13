# Overall Description

Part of the MyCilinder Software Requirements Specification (SRS).
Related: [Functional Requirements](./functional-requirements.md) · [Non-Functional Requirements](./non-functional-requirements.md) · [Project charter](../../README.md)

---

## 1. Product Functions

MyCilinder is a video-on-demand (VOD) platform. Users upload videos, the system transcodes them asynchronously into adaptive-bitrate HLS, and any viewer can browse, search, watch, and interact with them. Live streaming is out of scope.

**Product perspective.** The product is a set of backend microservices (this repo) consumed by a Flutter client (Android + web, separate repo) over an API defined in `docs/openapi.yaml`. Infrastructure is provisioned by a separate Terraform repo. Media bytes never pass through the API: uploads go directly to S3 via presigned URLs and playback is served by CloudFront.

| # | Function | Summary | Detailed in |
|---|----------|---------|-------------|
| PF1 | **Accounts & authentication** | Register, verify email, log in/out, reset password, manage display name. Identity is managed by AWS Cognito behind an `/auth` facade. | FR-AUTH |
| PF2 | **Video upload** | Authenticated users upload a video directly to object storage, with title and description. Limits on size, duration, and resolution apply. | FR-UP |
| PF3 | **Transcoding** | Uploaded videos are validated and converted asynchronously to HLS (360p / 720p) plus a thumbnail. | FR-TR |
| PF4 | **Playback** | Watch a video with adaptive bitrate through the CDN; share it via a permalink; views are counted. | FR-PB |
| PF5 | **Browse & search** | Recent and most-viewed feeds, keyword search, and creator pages. | FR-BR |
| PF6 | **Interactions** | Comments, likes, and view/like/comment counters. | FR-IN |
| PF7 | **Creator studio** | See my uploads and their processing status; edit or delete them. | FR-CR |
| PF8 | **Reporting hooks** | Report a video or comment; operators can review reports and hide content. | FR-MOD |

---

## 2. User Classes and Characteristics

| Class | Description | Technical skill | Permissions | Priority |
|-------|-------------|-----------------|-------------|----------|
| **Guest** | Unauthenticated visitor, typically arriving via a shared link. | None assumed. | Browse, search, watch, read comments and counts. Cannot upload, comment, or like. | High |
| **Registered User** | Authenticated account holder. Primary consumer of the platform. | Low to moderate; familiar with mainstream video apps. | Everything a Guest can do, plus comment, like, report, and manage own profile. | High |
| **Creator** | A Registered User who uploads videos. Not a separate account type; the capability is available to every verified user. | Low to moderate. Expects upload progress and clear processing status. | Registered User permissions, plus upload, edit, and delete **own** videos and delete comments on own videos. | High |
| **Platform Operator** | A member of the development team operating the system (AWS console, dashboards, scripts). | High. | Review reports, hide content, view metrics and logs. There is **no admin UI in the MVP**; operator actions are performed through restricted endpoints or operational scripts. `[TBC]` | Medium |

Non-human actors that appear in use cases: the **Transcoder worker** (consumes queue jobs) and **Cognito** (identity provider).

---

## 3. Operating Environment

**Clients**
- **Android:** Flutter app, sideloaded as an APK for the demo. Minimum Android version is defined by the Frontend repo (Flutter default floor).
- **Web:** Latest two versions of Chrome, Edge, Firefox, and Safari. Hosted on Vercel (free tier) only if a web demo is needed.
- **HLS note:** Safari plays HLS natively. Other browsers need an MSE-based HLS player (e.g. hls.js-style). Flutter-web HLS playback must be validated early in Design (see R5, walking skeleton).
- **Network:** HTTPS only. Clients must tolerate mobile-grade bandwidth; adaptive bitrate handles variation.

**Server side**
- **Cloud:** AWS, single region. The region is chosen in Design (service availability, price, and latency to the user base).
- **Compute:** Linux containers on ECS Fargate, built per service and stored in ECR.
- **Runtimes:** Go (Catalog, Interactions, Auth facade) and Python (Upload/Ingest, Transcoder worker). See README §6.
- **Data:** RDS PostgreSQL, ElastiCache Redis, S3 (`raw` and `output` buckets), SQS, CloudFront, Cognito.
- **Provisioning & delivery:** Terraform (separate repo), GitHub Actions CI/CD.

**Environments** `[TBC]`
- **Local:** Docker Compose for developing individual services.
- **AWS development:** AWS environment for development.
- **AWS demo:** an environment sized for demo scale (see the Back-of-envelope section in the NFR document).

---

## 4. Design and Implementation Constraints

| ID | Constraint | Source |
|----|------------|--------|
| C-01 | Infrastructure must run on **AWS**. | Team decision |
| C-02 | Total spend must stay within the school-provided **~100–140 AWS credits**. Budget alarms are mandatory. | Project (O3, R1) |
| C-03 | Three repositories (polyrepo): Infra (Terraform), Backend, Frontend (Flutter). | Team decision |
| C-04 | Backend limited to **two runtime languages: Go and Python**. | Team decision |
| C-05 | Authentication via **AWS Cognito**, fronted by an `/auth` facade. | Team decision |
| C-06 | **Waterfall** process. `openapi.yaml` is frozen at the end of Design; later changes require a change request. | Course / project |
| C-07 | **No live streaming**, ads, recommendations, DRM, or advanced moderation. | Scope (README §1.1) |
| C-08 | **Media data path stays off application compute** (presigned S3 uploads, CloudFront delivery). | Architectural principle |
| C-09 | MVP transcodes to **360p and 720p only**, with caps on input size, duration, and resolution. | Cost control (R1, R2) |
| C-10 | Web frontend, if hosted, uses **Vercel free tier**; otherwise Android APK is sideloaded. | Team decision |
| C-11 | No public write access to buckets; media is served only through signed URLs. | Security (R7) |
| C-12 | **Regulatory:** no formal compliance program (e.g. Mexican personal-data law LFPDPPP, copyright takedown) is in scope. The system limits risk by collecting minimal personal data and providing reporting hooks. Legal review is out of scope.  | Scope decision |

---

## 5. Assumptions and Dependencies

### 5.1 Assumptions

| ID | Assumption |
|----|------------|
| A-01 | Credits come from a **standard AWS account** where the team can create IAM roles, Cognito pools, CloudFront distributions, etc. |
| A-02 | Credits are enough for a short-lived demo at the **demo scale** defined in the NFR document, not for sustained load. |
| A-03 | Demo traffic is small and curated: tens of users, a small catalog of short videos. |
| A-04 | The 100k–1M DAU figures are a **design target**, validated by calculation (back-of-envelope) and architecture review, not by production traffic. |
| A-05 | Every registered user may upload (no creator approval). |
| A-06 | Guests may watch public videos without logging in. |
| A-07 | All published videos are public. Private and unlisted visibility are out of scope. |
| A-08 | Comments are flat (no replies, no comment likes) in the MVP. |
| A-09 | Users are expected to be 13 or older. Age verification is not implemented. |
| A-10 | Counters (views, likes) may be **eventually consistent** by a few seconds. |

### 5.2 Dependencies

**External services**

| Dependency | Used for | Impact if unavailable |
|------------|----------|----------------------|
| AWS Cognito | Identity and tokens | No login or registration. Already-issued tokens keep working until expiry. |
| Amazon S3 | Raw and output media storage | No upload or playback origin. |
| Amazon CloudFront | Media delivery | Playback degraded or down. |
| Amazon SQS | Transcode job queue | New uploads queue up unprocessed. |
| Amazon ECS Fargate / ECR | Running services | Service outage. |
| Amazon RDS PostgreSQL | Metadata, comments, likes | Core read path down. |
| ElastiCache Redis | Counters and hot cache | Degrades to DB reads (see NFR REL). |
| AWS MediaConvert **or** ffmpeg on Fargate | Transcoding (decided in Design) | Videos stay in PROCESSING. |
| Vercel (optional) | Web client hosting | Web demo unavailable. Android demo is the fallback. |
| GitHub Actions | CI/CD | Builds and deploys blocked. Manual deploy is the fallback. |

**Cross-repo dependencies**
- Backend deployment depends on the **Infra** repo having provisioned networking, data stores, queues, and buckets.
- The Frontend depends on a stable `openapi.yaml` from this repo.

**Platform constraints to watch**
- New or educational AWS accounts often have **low default service quotas** (e.g. Fargate vCPU, concurrent tasks). These can block scale-up demonstrations. Check quotas in Design.
