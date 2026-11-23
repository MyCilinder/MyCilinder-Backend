-- Grant checks for the per-service roles (Data Design §1.1, SEC-15). Run as postgres
-- after all migrations:
--
--   docker compose -f deploy/local/docker-compose.yml exec -T postgres \
--     psql -v ON_ERROR_STOP=1 -U postgres -d mycilinder < deploy/local/checks/grants.sql
--
-- Exits non-zero on the first failed check.

DO $$
DECLARE
    c record;
    failures int := 0;
BEGIN
    FOR c IN
        SELECT * FROM (VALUES
            -- catalog.videos: Catalog changes rows, Upload creates them, nobody deletes.
            ('catalog_svc',      'catalog.videos',              'SELECT', true),
            ('catalog_svc',      'catalog.videos',              'UPDATE', true),
            ('catalog_svc',      'catalog.videos',              'INSERT', false),
            ('catalog_svc',      'catalog.videos',              'DELETE', false),
            ('upload_svc',       'catalog.videos',              'INSERT', true),
            ('upload_svc',       'catalog.videos',              'UPDATE', false),
            ('upload_svc',       'catalog.videos',              'SELECT', false),
            ('upload_svc',       'catalog.videos',              'DELETE', false),
            ('interactions_svc', 'catalog.videos',              'SELECT', false),
            ('auth_svc',         'catalog.videos',              'SELECT', false),
            -- Cross-schema reads go through views only.
            ('interactions_svc', 'catalog.v_video_visibility',  'SELECT', true),
            ('catalog_svc',      'interactions.v_video_stats',  'SELECT', true),
            ('catalog_svc',      'interactions.video_stats',    'SELECT', false),
            ('catalog_svc',      'auth.v_user_public',          'SELECT', true),
            ('interactions_svc', 'auth.v_user_public',          'SELECT', true),
            ('catalog_svc',      'auth.users',                  'SELECT', false),
            ('interactions_svc', 'auth.users',                  'SELECT', false),
            ('upload_svc',       'auth.v_user_public',          'SELECT', false),
            -- Each service writes only its own schema.
            ('upload_svc',       'upload.daily_quota',          'UPDATE', true),
            ('catalog_svc',      'upload.upload_requests',      'SELECT', false),
            ('interactions_svc', 'interactions.likes',          'DELETE', true),
            ('interactions_svc', 'interactions.comments',       'DELETE', false),
            ('catalog_svc',      'interactions.likes',          'INSERT', false),
            -- audit_log: INSERT for auth, catalog, interactions; never UPDATE or DELETE.
            ('auth_svc',         'audit.audit_log',             'INSERT', true),
            ('catalog_svc',      'audit.audit_log',             'INSERT', true),
            ('interactions_svc', 'audit.audit_log',             'INSERT', true),
            ('upload_svc',       'audit.audit_log',             'INSERT', false),
            ('auth_svc',         'audit.audit_log',             'UPDATE', false),
            ('catalog_svc',      'audit.audit_log',             'UPDATE', false),
            ('interactions_svc', 'audit.audit_log',             'UPDATE', false),
            ('upload_svc',       'audit.audit_log',             'UPDATE', false),
            ('auth_svc',         'audit.audit_log',             'DELETE', false),
            ('catalog_svc',      'audit.audit_log',             'DELETE', false),
            ('interactions_svc', 'audit.audit_log',             'DELETE', false),
            ('upload_svc',       'audit.audit_log',             'DELETE', false),
            ('catalog_svc',      'audit.audit_log',             'SELECT', false)
        ) AS t(role_name, object_name, privilege, expected)
    LOOP
        IF has_table_privilege(c.role_name, c.object_name, c.privilege) IS DISTINCT FROM c.expected THEN
            failures := failures + 1;
            RAISE WARNING 'FAIL: % % on % should be %',
                c.role_name, c.privilege, c.object_name,
                CASE WHEN c.expected THEN 'granted' ELSE 'denied' END;
        END IF;
    END LOOP;

    -- Schema USAGE, needed in addition to the table grant.
    FOR c IN
        SELECT * FROM (VALUES
            ('catalog_svc',      'catalog',      true),
            ('catalog_svc',      'interactions', true),
            ('catalog_svc',      'auth',         true),
            ('catalog_svc',      'audit',        true),
            ('catalog_svc',      'upload',       false),
            ('interactions_svc', 'catalog',      true),
            ('interactions_svc', 'upload',       false),
            ('upload_svc',       'catalog',      true),
            ('upload_svc',       'auth',         false),
            ('upload_svc',       'audit',        false)
        ) AS t(role_name, schema_name, expected)
    LOOP
        IF has_schema_privilege(c.role_name, c.schema_name, 'USAGE') IS DISTINCT FROM c.expected THEN
            failures := failures + 1;
            RAISE WARNING 'FAIL: % USAGE on schema % should be %',
                c.role_name, c.schema_name,
                CASE WHEN c.expected THEN 'granted' ELSE 'denied' END;
        END IF;
    END LOOP;

    IF failures > 0 THEN
        RAISE EXCEPTION '% grant check(s) failed', failures;
    END IF;
    RAISE NOTICE 'All grant checks passed';
END
$$;
