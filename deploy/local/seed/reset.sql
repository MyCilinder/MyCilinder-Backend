-- Deletes the mock data created by seed.sql, and nothing else: only the fixed seed ids
-- (5eed0000-…, 5eed1000-…, 5eed2000-…). Local development only. Run the seed again
-- afterwards to get a fresh dataset:
--
--   docker compose -f deploy/local/docker-compose.yml exec -T postgres \
--     psql -v ON_ERROR_STOP=1 -U postgres -d mycilinder < deploy/local/seed/reset.sql
--   docker compose -f deploy/local/docker-compose.yml up seed
--
-- Your real test user (SEED_OWNER_SUB) is a Cognito profile and is not deleted.
-- audit_log rows about seed videos are kept: the trail is append-only.

BEGIN;
DELETE FROM interactions.comments    WHERE id::text       LIKE '5eed2000-%';
DELETE FROM interactions.likes       WHERE video_id::text LIKE '5eed1000-%';
DELETE FROM interactions.video_stats WHERE video_id::text LIKE '5eed1000-%';
DELETE FROM catalog.videos           WHERE id::text       LIKE '5eed1000-%';
DELETE FROM auth.users               WHERE id::text       LIKE '5eed0000-%';
COMMIT;
