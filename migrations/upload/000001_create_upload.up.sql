-- Upload / Ingest schema (Data Design §1.1, §1.3).
--
-- user_id and video_id reference other schemas logically, with no foreign keys
-- (MAINT-09).

CREATE SCHEMA IF NOT EXISTS upload;

-- One row per upload. A retried request with the same Idempotency-Key returns the
-- existing videoId and does not consume quota (REL-15).
CREATE TABLE upload.upload_requests (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             uuid        NOT NULL,
    video_id            uuid        NOT NULL UNIQUE,
    idempotency_key     text        NOT NULL
        CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
    multipart_upload_id text,       -- null for a single PUT
    created_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key)
);

-- Uploads per user per UTC calendar day (BR-LIM). The limit itself is configuration and
-- lives in the atomic upsert, not in a CHECK:
--   INSERT ... ON CONFLICT (user_id, day) DO UPDATE SET uploads = daily_quota.uploads + 1
--   WHERE daily_quota.uploads < $limit RETURNING uploads
CREATE TABLE upload.daily_quota (
    user_id uuid    NOT NULL,
    day     date    NOT NULL,
    uploads integer NOT NULL DEFAULT 0 CHECK (uploads >= 0),
    PRIMARY KEY (user_id, day)
);

REVOKE ALL ON SCHEMA upload FROM PUBLIC;

-- ON CONFLICT DO UPDATE with WHERE and RETURNING needs SELECT and UPDATE as well.
GRANT USAGE ON SCHEMA upload TO upload_svc;
GRANT SELECT, INSERT, UPDATE ON upload.upload_requests TO upload_svc;
GRANT SELECT, INSERT, UPDATE ON upload.daily_quota     TO upload_svc;
