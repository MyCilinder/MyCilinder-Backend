-- The schema that owns a view grants it to its readers, so no migration depends on
-- another schema's migrations having run. Supersedes the note in 000001 that consumer
-- services grant themselves access.

GRANT USAGE ON SCHEMA auth TO catalog_svc, interactions_svc;
GRANT SELECT ON auth.v_user_public TO catalog_svc, interactions_svc;
