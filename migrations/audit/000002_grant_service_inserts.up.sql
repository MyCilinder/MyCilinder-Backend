-- Catalog and Interactions write audit rows (Data Design §1.3). INSERT only: no role has
-- UPDATE or DELETE on the trail (SEC-15), and inserts must not use RETURNING.

GRANT USAGE ON SCHEMA audit TO catalog_svc, interactions_svc;
GRANT INSERT ON audit.audit_log TO catalog_svc, interactions_svc;
