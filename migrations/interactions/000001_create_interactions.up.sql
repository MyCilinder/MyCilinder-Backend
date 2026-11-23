-- Interactions schema (Data Design §1.1, §1.3).
--
-- video_id, author_id, user_id, reporter_id, target_id and resolved_by reference other
-- schemas logically, with no foreign keys (MAINT-09).

CREATE SCHEMA IF NOT EXISTS interactions;

CREATE TYPE interactions.report_target AS ENUM ('VIDEO', 'COMMENT');
CREATE TYPE interactions.report_reason AS ENUM (
    'SPAM', 'HARASSMENT', 'SEXUAL_CONTENT', 'VIOLENCE', 'COPYRIGHT', 'OTHER'
);
CREATE TYPE interactions.report_status AS ENUM ('OPEN', 'RESOLVED', 'DISMISSED');

-- Flat comments (A-08). Users cannot edit or delete them; an operator hides one by
-- setting hidden_at, which removes it from lists and counts.
CREATE TABLE interactions.comments (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    video_id   uuid        NOT NULL,
    author_id  uuid        NOT NULL,
    body       text        NOT NULL CHECK (char_length(body) BETWEEN 1 AND 1000),
    created_at timestamptz NOT NULL DEFAULT now(),
    hidden_at  timestamptz
);

-- Comment list (FR-IN-02).
CREATE INDEX comments_video_visible_idx
    ON interactions.comments (video_id, created_at DESC, id DESC)
    WHERE hidden_at IS NULL;

-- Source of truth for likes. The primary key enforces one like per user and video
-- (BR-04); INSERT ... ON CONFLICT DO NOTHING makes liking idempotent (FR-IN-05).
CREATE TABLE interactions.likes (
    video_id   uuid        NOT NULL,
    user_id    uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (video_id, user_id)
);

-- Durable counters, updated in batches from Redis (Data Design §3.2).
CREATE TABLE interactions.video_stats (
    video_id      uuid        PRIMARY KEY,
    view_count    bigint      NOT NULL DEFAULT 0 CHECK (view_count >= 0),
    like_count    bigint      NOT NULL DEFAULT 0 CHECK (like_count >= 0),
    comment_count bigint      NOT NULL DEFAULT 0 CHECK (comment_count >= 0),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Most-viewed feed (FR-BR-02).
CREATE INDEX video_stats_most_viewed_idx
    ON interactions.video_stats (view_count DESC, video_id DESC);

CREATE TABLE interactions.reports (
    id          uuid                       PRIMARY KEY DEFAULT gen_random_uuid(),
    reporter_id uuid                       NOT NULL,
    target_type interactions.report_target NOT NULL,
    target_id   uuid                       NOT NULL,
    reason      interactions.report_reason NOT NULL,
    details     text                       CHECK (char_length(details) <= 1000),
    status      interactions.report_status NOT NULL DEFAULT 'OPEN',
    created_at  timestamptz                NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    resolved_by uuid                       -- operator users.id
);

-- One open report per reporter and target (FR-MOD-02).
CREATE UNIQUE INDEX reports_open_unique_idx
    ON interactions.reports (reporter_id, target_type, target_id)
    WHERE status = 'OPEN';

-- Operator queue (FR-MOD-03).
CREATE INDEX reports_queue_idx ON interactions.reports (status, created_at);

-- Read-only view for Catalog's feeds and video pages.
CREATE VIEW interactions.v_video_stats AS
    SELECT video_id, view_count, like_count, comment_count
    FROM interactions.video_stats;

REVOKE ALL ON SCHEMA interactions FROM PUBLIC;

GRANT USAGE ON SCHEMA interactions TO interactions_svc;
GRANT SELECT, INSERT, UPDATE ON interactions.comments    TO interactions_svc;
GRANT SELECT, INSERT, DELETE ON interactions.likes       TO interactions_svc;
GRANT SELECT, INSERT, UPDATE ON interactions.video_stats TO interactions_svc;
GRANT SELECT, INSERT, UPDATE ON interactions.reports     TO interactions_svc;

-- The schema that owns a view grants it to its readers.
GRANT USAGE ON SCHEMA interactions TO catalog_svc;
GRANT SELECT ON interactions.v_video_stats TO catalog_svc;
