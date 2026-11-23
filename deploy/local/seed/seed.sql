-- Local mock data for development. Never run in AWS, and never part of migrations/.
--
-- Runs as postgres in the Compose `seed` one-shot after every migration, on every
-- `docker compose up`. It is idempotent: rows use fixed ids and ON CONFLICT DO NOTHING,
-- so running it again leaves edited or job-processed rows alone. To start over, run
-- reset.sql (deletes only these fixed ids) and then the seed again.
--
-- The psql variable seed_owner_sub (from SEED_OWNER_SUB) is the Cognito sub of your test
-- user. Eight videos in mixed statuses belong to it, so the owner routes can be tried with
-- a real token. If it is empty they belong to a placeholder user; setting it later and
-- re-running the seed moves those eight videos to the real user.
--
-- Id scheme (all valid v4 UUIDs):
--   users   5eed0000-0000-4000-8000-0000000000NN
--   videos  5eed1000-0000-4000-8000-0000000000NN
--   comments 5eed2000-0000-4000-8000-0000000000NN

\set ON_ERROR_STOP on
\if :{?seed_owner_sub}
\else
\set seed_owner_sub ''
\endif

SELECT COALESCE(NULLIF(:'seed_owner_sub', ''), '5eed0000-0000-4000-8000-000000000009')::uuid AS seed_owner
\gset

BEGIN;

-- Users. The placeholder owner exists even when a real sub is given, so its id stays
-- valid in old cursors and links.
INSERT INTO auth.users (id, display_name) VALUES
    ('5eed0000-0000-4000-8000-000000000001', 'Diego Gómez'),
    ('5eed0000-0000-4000-8000-000000000002', 'Darío Lucciano'),
    ('5eed0000-0000-4000-8000-000000000003', 'Alexis Flores'),
    ('5eed0000-0000-4000-8000-000000000004', 'María José Peña'),
    ('5eed0000-0000-4000-8000-000000000005', 'Canal ITESO'),
    ('5eed0000-0000-4000-8000-000000000009', 'Seed Owner')
ON CONFLICT (id) DO NOTHING;

-- The real test user. If the auth facade already created the profile, it is kept.
INSERT INTO auth.users (id, display_name) VALUES (:'seed_owner', 'Usuario de prueba')
ON CONFLICT (id) DO NOTHING;

-- Videos owned by the seeded users: every status, Spanish text for search.
-- Columns: id, owner, title, description, status, failure_reason, duration, renditions,
-- created_at, published_at, deleted_at.
INSERT INTO catalog.videos
    (id, owner_id, title, description, status, failure_reason, content_type, size_bytes,
     duration_seconds, renditions, created_at, updated_at, published_at, deleted_at)
SELECT v.id::uuid, v.owner::uuid, v.title, v.description, v.status::catalog.video_status,
       v.reason, 'video/mp4', 50000000, v.duration, v.renditions::text[],
       v.created, COALESCE(v.deleted, v.published, v.created), v.published, v.deleted
FROM (VALUES
    -- READY, public. 01 and 02 get real HLS files from scripts/seed-media.sh.
    ('5eed1000-0000-4000-8000-000000000001', '5eed0000-0000-4000-8000-000000000005',
     'Recorrido por el campus del ITESO', 'Un recorrido completo por las instalaciones, la biblioteca y los jardines.',
     'READY', NULL, 10, '{360p,720p}', now() - interval '20 days', now() - interval '20 days' + interval '10 min', NULL::timestamptz),
    ('5eed1000-0000-4000-8000-000000000002', '5eed0000-0000-4000-8000-000000000001',
     'Tutorial de Go: concurrencia con goroutines', 'Explicación de canales, goroutines y el paquete sync, con ejemplos.',
     'READY', NULL, 10, '{360p,720p}', now() - interval '18 days', now() - interval '18 days' + interval '8 min', NULL),
    ('5eed1000-0000-4000-8000-000000000003', '5eed0000-0000-4000-8000-000000000002',
     'Receta de tacos al pastor', 'Cómo marinar la carne y preparar la piña. Receta tradicional de Guadalajara.',
     'READY', NULL, 412, '{360p,720p}', now() - interval '15 days', now() - interval '15 days' + interval '12 min', NULL),
    ('5eed1000-0000-4000-8000-000000000004', '5eed0000-0000-4000-8000-000000000003',
     'Sistemas escalables: introducción', 'Primera clase del curso. Arquitectura, réplicas y caché. Incluye un recorrido por AWS.',
     'READY', NULL, 1830, '{360p,720p}', now() - interval '14 days', now() - interval '14 days' + interval '20 min', NULL),
    ('5eed1000-0000-4000-8000-000000000005', '5eed0000-0000-4000-8000-000000000004',
     'Música en vivo: guitarra acústica', 'Sesión grabada en la cafetería. Canciones de autor.',
     'READY', NULL, 655, '{360p,720p}', now() - interval '12 days', now() - interval '12 days' + interval '9 min', NULL),
    ('5eed1000-0000-4000-8000-000000000006', '5eed0000-0000-4000-8000-000000000005',
     'Bienvenida a estudiantes de nuevo ingreso', 'Mensaje del rector y recorrido rápido por los edificios principales.',
     'READY', NULL, 300, '{360p,720p}', now() - interval '10 days', now() - interval '10 days' + interval '7 min', NULL),
    ('5eed1000-0000-4000-8000-000000000007', '5eed0000-0000-4000-8000-000000000001',
     'Tutorial de PostgreSQL: búsqueda de texto completo', 'tsvector, tsquery y el diccionario español. Cómo ordenar por relevancia.',
     'READY', NULL, 980, '{360p,720p}', now() - interval '9 days', now() - interval '9 days' + interval '15 min', NULL),
    ('5eed1000-0000-4000-8000-000000000008', '5eed0000-0000-4000-8000-000000000002',
     'Paseo en bicicleta por Chapultepec', 'Ruta dominical por la avenida. Cámara en el manubrio.',
     'READY', NULL, 1204, '{360p}', now() - interval '7 days', now() - interval '7 days' + interval '11 min', NULL),
    ('5eed1000-0000-4000-8000-000000000009', '5eed0000-0000-4000-8000-000000000003',
     'Cómo funciona una CDN', 'CloudFront, caché en el borde y cookies firmadas explicadas con dibujos.',
     'READY', NULL, 742, '{360p,720p}', now() - interval '6 days', now() - interval '6 days' + interval '10 min', NULL),
    ('5eed1000-0000-4000-8000-000000000010', '5eed0000-0000-4000-8000-000000000004',
     'Fotografía nocturna con el celular', 'Consejos de exposición y estabilización. Ejemplos en el centro histórico.',
     'READY', NULL, 433, '{360p,720p}', now() - interval '5 days', now() - interval '5 days' + interval '6 min', NULL),
    ('5eed1000-0000-4000-8000-000000000011', '5eed0000-0000-4000-8000-000000000005',
     'Feria de proyectos de ingeniería', 'Los equipos presentan sus prototipos. Robots, drones y aplicaciones.',
     'READY', NULL, 2410, '{360p,720p}', now() - interval '4 days', now() - interval '4 days' + interval '25 min', NULL),
    ('5eed1000-0000-4000-8000-000000000012', '5eed0000-0000-4000-8000-000000000001',
     'Tutorial: Docker Compose para desarrollo local', 'Servicios, volúmenes y migraciones de un solo uso.',
     'READY', NULL, 866, '{360p,720p}', now() - interval '3 days', now() - interval '3 days' + interval '9 min', NULL),
    ('5eed1000-0000-4000-8000-000000000013', '5eed0000-0000-4000-8000-000000000002',
     'Mercado de San Juan de Dios', 'Comida, artesanías y un recorrido por los tres pisos del mercado.',
     'READY', NULL, 590, '{360p,720p}', now() - interval '2 days', now() - interval '2 days' + interval '8 min', NULL),
    ('5eed1000-0000-4000-8000-000000000014', '5eed0000-0000-4000-8000-000000000003',
     'Árboles de decisión en cinco minutos', 'Introducción rápida a la clasificación con ejemplos en Python.',
     'READY', NULL, 301, '{360p}', now() - interval '1 day', now() - interval '1 day' + interval '5 min', NULL),
    -- HIDDEN by an operator.
    ('5eed1000-0000-4000-8000-000000000015', '5eed0000-0000-4000-8000-000000000004',
     'Video reportado por spam', 'Promociones repetidas.',
     'HIDDEN', NULL, 120, '{360p,720p}', now() - interval '11 days', now() - interval '11 days' + interval '5 min', NULL),
    -- DELETED 8 days ago: their outputs are due for cleanup (retention 7 days).
    ('5eed1000-0000-4000-8000-000000000016', '5eed0000-0000-4000-8000-000000000001',
     'Borrador eliminado', 'Video que el creador borró.',
     'DELETED', NULL, 200, '{360p,720p}', now() - interval '30 days', now() - interval '29 days', now() - interval '8 days'),
    ('5eed1000-0000-4000-8000-000000000017', '5eed0000-0000-4000-8000-000000000002',
     'Clase repetida', 'Se subió dos veces por error.',
     'DELETED', NULL, 1500, '{360p,720p}', now() - interval '25 days', now() - interval '24 days', now() - interval '8 days'),
    -- Stale: PENDING_UPLOAD older than 24 h (expiry job) and PROCESSING older than 2 h (sweep).
    ('5eed1000-0000-4000-8000-000000000018', '5eed0000-0000-4000-8000-000000000003',
     'Subida abandonada', '',
     'PENDING_UPLOAD', NULL, NULL, '{}', now() - interval '30 hours', NULL, NULL),
    ('5eed1000-0000-4000-8000-000000000019', '5eed0000-0000-4000-8000-000000000004',
     'Procesamiento atascado', '',
     'PROCESSING', NULL, NULL, '{}', now() - interval '3 hours', NULL, NULL),
    ('5eed1000-0000-4000-8000-000000000020', '5eed0000-0000-4000-8000-000000000005',
     'Archivo dañado', '',
     'FAILED', 'El archivo de video está dañado o incompleto.', NULL, '{}', now() - interval '6 days', NULL, NULL),
    ('5eed1000-0000-4000-8000-000000000029', '5eed0000-0000-4000-8000-000000000001',
     'Subida que nunca llegó', '',
     'EXPIRED', NULL, NULL, '{}', now() - interval '10 days', NULL, NULL),
    -- Recent PENDING_UPLOAD: the target for the status-event demo (Task 10).
    ('5eed1000-0000-4000-8000-000000000030', '5eed0000-0000-4000-8000-000000000002',
     'Nuevo video en camino', 'Se está subiendo.',
     'PENDING_UPLOAD', NULL, NULL, '{}', now() - interval '5 minutes', NULL, NULL)
) AS v(id, owner, title, description, status, reason, duration, renditions, created, published, deleted)
ON CONFLICT (id) DO NOTHING;

-- Videos owned by the test user (SEED_OWNER_SUB), mixed statuses. owner_id follows the
-- variable on every run; nothing else is overwritten.
INSERT INTO catalog.videos
    (id, owner_id, title, description, status, failure_reason, content_type, size_bytes,
     duration_seconds, renditions, created_at, updated_at, published_at, deleted_at)
SELECT v.id::uuid, :'seed_owner', v.title, v.description, v.status::catalog.video_status,
       v.reason, 'video/mp4', 30000000, v.duration, v.renditions::text[],
       v.created, COALESCE(v.published, v.created), v.published, NULL
FROM (VALUES
    ('5eed1000-0000-4000-8000-000000000021', 'Mi primer video', 'Prueba de la plataforma.',
     'READY', NULL, 95, '{360p,720p}', now() - interval '8 days', now() - interval '8 days' + interval '4 min'),
    ('5eed1000-0000-4000-8000-000000000022', 'Tutorial de Flutter: reproductor HLS', 'Cómo reproducir video adaptativo en Android y web.',
     'READY', NULL, 720, '{360p,720p}', now() - interval '6 days', now() - interval '6 days' + interval '9 min'),
    ('5eed1000-0000-4000-8000-000000000023', 'Recorrido por el laboratorio de redes', 'Switches, routers y el rack del laboratorio.',
     'READY', NULL, 410, '{360p}', now() - interval '3 days', now() - interval '3 days' + interval '6 min'),
    ('5eed1000-0000-4000-8000-000000000024', 'Video en procesamiento', 'Todavía se está transcodificando.',
     'PROCESSING', NULL, NULL, '{}', now() - interval '20 minutes', NULL),
    ('5eed1000-0000-4000-8000-000000000025', 'Subida pendiente', '',
     'PENDING_UPLOAD', NULL, NULL, '{}', now() - interval '10 minutes', NULL),
    ('5eed1000-0000-4000-8000-000000000026', 'Video con error', 'Falló la transcodificación.',
     'FAILED', 'El formato del archivo no es compatible.', NULL, '{}', now() - interval '2 days', NULL),
    ('5eed1000-0000-4000-8000-000000000027', 'Video ocultado por moderación', 'Un operador lo ocultó.',
     'HIDDEN', NULL, 230, '{360p,720p}', now() - interval '9 days', now() - interval '9 days' + interval '5 min'),
    ('5eed1000-0000-4000-8000-000000000028', 'Subida expirada', '',
     'EXPIRED', NULL, NULL, '{}', now() - interval '4 days', NULL)
) AS v(id, title, description, status, reason, duration, renditions, created, published)
ON CONFLICT (id) DO UPDATE SET owner_id = EXCLUDED.owner_id;

-- Counters with varied values, so most_viewed has a clear order.
INSERT INTO interactions.video_stats (video_id, view_count, like_count, comment_count)
SELECT v.id::uuid, v.views, v.likes, v.comments
FROM (VALUES
    ('5eed1000-0000-4000-8000-000000000001', 15230, 3, 2),
    ('5eed1000-0000-4000-8000-000000000002',  8410, 2, 1),
    ('5eed1000-0000-4000-8000-000000000003', 22950, 2, 1),
    ('5eed1000-0000-4000-8000-000000000004',  1204, 1, 1),
    ('5eed1000-0000-4000-8000-000000000005',   530, 0, 0),
    ('5eed1000-0000-4000-8000-000000000006',  9800, 1, 0),
    ('5eed1000-0000-4000-8000-000000000007',  3120, 0, 0),
    ('5eed1000-0000-4000-8000-000000000008',   777, 0, 0),
    ('5eed1000-0000-4000-8000-000000000009',  4501, 0, 0),
    ('5eed1000-0000-4000-8000-000000000010',    88, 0, 0),
    ('5eed1000-0000-4000-8000-000000000011', 12011, 0, 0),
    ('5eed1000-0000-4000-8000-000000000012',  2048, 0, 0),
    ('5eed1000-0000-4000-8000-000000000013',   640, 0, 0),
    ('5eed1000-0000-4000-8000-000000000014',    15, 0, 0),
    ('5eed1000-0000-4000-8000-000000000015',   300, 0, 0),
    ('5eed1000-0000-4000-8000-000000000021',    42, 1, 1),
    ('5eed1000-0000-4000-8000-000000000022',   310, 1, 0),
    ('5eed1000-0000-4000-8000-000000000023',     9, 0, 0)
) AS v(id, views, likes, comments)
ON CONFLICT (video_id) DO NOTHING;
-- Videos 13 and 14 have no likes; video 14's low count and the missing stats rows of
-- other videos exercise the COALESCE(…, 0) paths.

INSERT INTO interactions.likes (video_id, user_id)
SELECT v.video::uuid, v.usr::uuid
FROM (VALUES
    ('5eed1000-0000-4000-8000-000000000001', '5eed0000-0000-4000-8000-000000000001'),
    ('5eed1000-0000-4000-8000-000000000001', '5eed0000-0000-4000-8000-000000000002'),
    ('5eed1000-0000-4000-8000-000000000001', '5eed0000-0000-4000-8000-000000000003'),
    ('5eed1000-0000-4000-8000-000000000002', '5eed0000-0000-4000-8000-000000000003'),
    ('5eed1000-0000-4000-8000-000000000002', '5eed0000-0000-4000-8000-000000000004'),
    ('5eed1000-0000-4000-8000-000000000003', '5eed0000-0000-4000-8000-000000000001'),
    ('5eed1000-0000-4000-8000-000000000003', '5eed0000-0000-4000-8000-000000000005'),
    ('5eed1000-0000-4000-8000-000000000004', '5eed0000-0000-4000-8000-000000000004'),
    ('5eed1000-0000-4000-8000-000000000006', '5eed0000-0000-4000-8000-000000000002'),
    ('5eed1000-0000-4000-8000-000000000021', '5eed0000-0000-4000-8000-000000000001'),
    ('5eed1000-0000-4000-8000-000000000022', '5eed0000-0000-4000-8000-000000000003')
) AS v(video, usr)
ON CONFLICT DO NOTHING;

INSERT INTO interactions.comments (id, video_id, author_id, body, created_at)
SELECT v.id::uuid, v.video::uuid, v.author::uuid, v.body, now() - v.age::interval
FROM (VALUES
    ('5eed2000-0000-4000-8000-000000000001', '5eed1000-0000-4000-8000-000000000001', '5eed0000-0000-4000-8000-000000000001', '¡Qué bonito se ve el campus!', '19 days'),
    ('5eed2000-0000-4000-8000-000000000002', '5eed1000-0000-4000-8000-000000000001', '5eed0000-0000-4000-8000-000000000004', 'Me faltó ver la biblioteca nueva.', '18 days'),
    ('5eed2000-0000-4000-8000-000000000003', '5eed1000-0000-4000-8000-000000000002', '5eed0000-0000-4000-8000-000000000002', 'Muy claro lo de los canales, gracias.', '17 days'),
    ('5eed2000-0000-4000-8000-000000000004', '5eed1000-0000-4000-8000-000000000003', '5eed0000-0000-4000-8000-000000000003', 'Mañana los preparo.', '14 days'),
    ('5eed2000-0000-4000-8000-000000000005', '5eed1000-0000-4000-8000-000000000004', '5eed0000-0000-4000-8000-000000000005', 'Excelente introducción.', '13 days'),
    ('5eed2000-0000-4000-8000-000000000006', '5eed1000-0000-4000-8000-000000000021', '5eed0000-0000-4000-8000-000000000002', '¡Bienvenido a la plataforma!', '7 days')
) AS v(id, video, author, body, age)
ON CONFLICT (id) DO NOTHING;

COMMIT;

-- Summary for the Compose log.
SELECT status, count(*) AS videos
FROM catalog.videos
WHERE id::text LIKE '5eed1000-%'
GROUP BY status
ORDER BY status;
