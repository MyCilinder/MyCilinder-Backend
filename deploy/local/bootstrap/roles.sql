-- Local development only. Creates one login role per service (Architecture §4.4).
--
-- Runs as the bootstrap-roles one-shot before every migrate-* container, on every
-- `docker compose up`, so it must be idempotent: it works on a fresh volume and on an
-- existing one. In AWS these roles and their passwords are created by an operator
-- bootstrap and stored in Secrets Manager; they are not part of the migrations because
-- migrations must not contain passwords.
--
-- Session settings are set per role (ALTER ROLE ... SET) instead of with SET in the
-- application, because session SET pins connections in RDS Proxy (Architecture §3).
-- The transcoder has no database access, so it has no role.

DO $$
DECLARE
    r record;
BEGIN
    FOR r IN
        SELECT * FROM (VALUES
            ('auth_svc',         'auth'),
            ('catalog_svc',      'catalog'),
            ('interactions_svc', 'interactions'),
            ('upload_svc',       'upload')
        ) AS t(role_name, schema_name)
    LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r.role_name) THEN
            EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', r.role_name, r.role_name || '_local');
        END IF;
        EXECUTE format('ALTER ROLE %I SET search_path = %I', r.role_name, r.schema_name);
        EXECUTE format('ALTER ROLE %I SET statement_timeout = %L', r.role_name, '5s');
        EXECUTE format('ALTER ROLE %I SET "TimeZone" = %L', r.role_name, 'UTC');
    END LOOP;
END
$$;
