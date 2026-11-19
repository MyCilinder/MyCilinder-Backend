-- Catalog schema (Data Design §1.1, §1.3).
--
-- Upload / Ingest inserts the row in PENDING_UPLOAD; every later change belongs to
-- Catalog (Architecture §2.3). Object keys are derived from id and never stored:
-- raw/{id}/source, videos/{id}/master.m3u8, thumbnails/{id}.jpg.
-- owner_id references auth.users logically, with no foreign key (MAINT-09).

CREATE SCHEMA IF NOT EXISTS catalog;

-- UPLOADED is transient and never stored (Functional Requirements §1.0).
CREATE TYPE catalog.video_status AS ENUM (
    'PENDING_UPLOAD',
    'PROCESSING',
    'READY',
    'FAILED',
    'HIDDEN',
    'DELETED',
    'EXPIRED'
);

CREATE TABLE catalog.videos (
    id                uuid                 PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id          uuid                 NOT NULL,
    title             text                 NOT NULL
        CHECK (char_length(title) BETWEEN 1 AND 100 AND btrim(title) <> ''),
    description       text                 NOT NULL DEFAULT ''
        CHECK (char_length(description) <= 5000),
    status            catalog.video_status NOT NULL DEFAULT 'PENDING_UPLOAD',
    failure_reason    text,
    content_type      text                 NOT NULL,
    size_bytes        bigint               NOT NULL
        CHECK (size_bytes BETWEEN 1 AND 1073741824),
    duration_seconds  integer              CHECK (duration_seconds > 0),
    renditions        text[]               NOT NULL DEFAULT '{}'
        CHECK (renditions <@ ARRAY['360p', '720p']::text[]),
    created_at        timestamptz          NOT NULL DEFAULT now(),
    updated_at        timestamptz          NOT NULL DEFAULT now(),
    published_at      timestamptz,          -- first READY
    deleted_at        timestamptz,
    -- Set by the cleanup job once the HLS outputs of a DELETED video are removed, so the
    -- job does not list S3 again for the same video (Catalog plan, decision 2).
    outputs_purged_at timestamptz,
    -- Titles rank above descriptions (FR-BR-03).
    search_vector     tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('spanish', title), 'A') ||
        setweight(to_tsvector('spanish', description), 'B')
    ) STORED
);

-- Search (FR-BR-03).
CREATE INDEX videos_search_idx ON catalog.videos USING gin (search_vector);

-- Recent feed (FR-BR-01). Both columns descend so the keyset predicate
-- (published_at, id) < ($k, $id) is served in index order.
CREATE INDEX videos_recent_idx ON catalog.videos (published_at DESC, id DESC)
    WHERE status = 'READY';

-- Creator studio and creator pages (FR-CR-01, FR-BR-07).
CREATE INDEX videos_owner_idx ON catalog.videos (owner_id, created_at DESC, id DESC);

-- Background jobs: expiry, stuck PROCESSING sweep, output cleanup.
CREATE INDEX videos_jobs_idx ON catalog.videos (status, updated_at)
    WHERE status IN ('PENDING_UPLOAD', 'PROCESSING', 'DELETED');

-- Read-only view for Interactions' video guard (Architecture §2.2).
CREATE VIEW catalog.v_video_visibility AS
    SELECT id, owner_id, status FROM catalog.videos;

REVOKE ALL ON SCHEMA catalog FROM PUBLIC;

-- Catalog changes rows but never creates or removes them (soft delete, BR-05).
GRANT USAGE ON SCHEMA catalog TO catalog_svc;
GRANT SELECT, UPDATE ON catalog.videos TO catalog_svc;

-- Upload / Ingest creates the row in PENDING_UPLOAD and nothing else. Its INSERT must not
-- use RETURNING, which would need SELECT.
GRANT USAGE ON SCHEMA catalog TO upload_svc;
GRANT INSERT ON catalog.videos TO upload_svc;

-- The schema that owns a view grants it to its readers.
GRANT USAGE ON SCHEMA catalog TO interactions_svc;
GRANT SELECT ON catalog.v_video_visibility TO interactions_svc;
