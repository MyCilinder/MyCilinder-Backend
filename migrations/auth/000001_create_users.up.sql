-- Auth facade schema (Data Design §1.1, §1.3).
--
-- users.id is the Cognito sub. Email and password stay in Cognito, so the profile holds
-- no personal data beyond the display name (SEC-12). Display names are not unique.

CREATE SCHEMA IF NOT EXISTS auth;

CREATE TABLE auth.users (
    id           uuid        PRIMARY KEY,
    display_name text        NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 50),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Read-only view for Catalog and Interactions. Those services' migrations grant SELECT on
-- it to their own roles once the roles exist.
CREATE VIEW auth.v_user_public AS
    SELECT id, display_name FROM auth.users;

REVOKE ALL ON SCHEMA auth FROM PUBLIC;

GRANT USAGE ON SCHEMA auth TO auth_svc;
GRANT SELECT, INSERT, UPDATE ON auth.users TO auth_svc;
