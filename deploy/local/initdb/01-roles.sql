-- Local development only. Runs once, when the Postgres volume is first created.
--
-- Creates one login role per service (Architecture §4.4). In AWS these roles and their
-- passwords are created by an operator bootstrap and stored in Secrets Manager; they are
-- not part of the migrations because migrations must not contain passwords.
--
-- Session settings are set per role (ALTER ROLE ... SET) instead of with SET in the
-- application, because session SET pins connections in RDS Proxy (Architecture §3).

CREATE ROLE auth_svc LOGIN PASSWORD 'auth_svc_local';
ALTER ROLE auth_svc SET search_path = auth;
ALTER ROLE auth_svc SET statement_timeout = '5s';
ALTER ROLE auth_svc SET "TimeZone" = 'UTC';
