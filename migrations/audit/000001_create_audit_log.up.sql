-- Shared append-only audit trail (Data Design §1.3, SEC-15).
--
-- Services hold INSERT only. No role has UPDATE or DELETE, so a service cannot alter the
-- trail. Inserts must not use RETURNING, which would need SELECT.
-- details never contains tokens, passwords, or emails (SEC-12).

CREATE SCHEMA IF NOT EXISTS audit;

CREATE TABLE audit.audit_log (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    service     text        NOT NULL,
    actor_id    uuid,
    action      text        NOT NULL,
    target_type text,
    target_id   uuid,
    details     jsonb       NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX audit_log_occurred_at_idx ON audit.audit_log (occurred_at);

REVOKE ALL ON SCHEMA audit FROM PUBLIC;
REVOKE ALL ON audit.audit_log FROM PUBLIC;

-- Auth facade. Catalog and Interactions get the same two grants in their own migrations.
GRANT USAGE ON SCHEMA audit TO auth_svc;
GRANT INSERT ON audit.audit_log TO auth_svc;
