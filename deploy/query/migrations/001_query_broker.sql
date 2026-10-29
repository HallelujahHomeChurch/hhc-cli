-- Preview migration. Apply only through the approved database owner/role.
-- No credentials, grants or policy rows are created. Runtime never auto-migrates.
CREATE SCHEMA IF NOT EXISTS hhc_query;
CREATE TABLE hhc_query.schema_version (version integer PRIMARY KEY CHECK (version=1));
INSERT INTO hhc_query.schema_version VALUES (1);
CREATE TABLE hhc_query.broker_settings (singleton boolean PRIMARY KEY CHECK(singleton), key_fingerprint text NOT NULL CHECK(length(key_fingerprint)=64));
CREATE TABLE hhc_query.caller_policy (
 caller_id text PRIMARY KEY CHECK (length(caller_id)=64),
 revision bigint NOT NULL CHECK (revision>0), enabled boolean NOT NULL DEFAULT false,
 hhc_mode text NOT NULL DEFAULT 'disabled' CHECK (hhc_mode IN ('disabled','public','human')),
 hhc_grant text NOT NULL DEFAULT '', hhc_quota text NOT NULL DEFAULT '',
 youtube_mode text NOT NULL DEFAULT 'disabled' CHECK (youtube_mode IN ('disabled','public','owner')),
 youtube_grant text NOT NULL DEFAULT '', youtube_quota text NOT NULL DEFAULT '',
 channel_id text NOT NULL DEFAULT '', requests_per_minute integer NOT NULL CHECK (requests_per_minute BETWEEN 1 AND 1000)
);
-- The runtime may lock/read authority, but must not gain policy UPDATE rights.
CREATE FUNCTION hhc_query.lock_policy(requested_caller text) RETURNS SETOF hhc_query.caller_policy
 LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,hhc_query,pg_temp AS
 'SELECT * FROM hhc_query.caller_policy WHERE caller_id=requested_caller FOR SHARE';
REVOKE ALL ON FUNCTION hhc_query.lock_policy(text) FROM PUBLIC;
CREATE TABLE hhc_query.grant_state (
 grant_id text PRIMARY KEY, owner_id text NOT NULL, source text NOT NULL CHECK(source IN ('hhc','youtube')),
 resource text NOT NULL, kind text NOT NULL CHECK(kind IN ('human','api_key','oauth')),
 scope text NOT NULL, generation bigint NOT NULL CHECK(generation>0),
 state text NOT NULL CHECK(state IN ('active','rotating','uncertain','revoked')),
 expires_at timestamptz NOT NULL, ciphertext bytea NOT NULL CHECK(octet_length(ciphertext)<=65536),
 rotation_started_at timestamptz, updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
-- Durable keyed identities survive grant deletion/uncertainty. Never truncate
-- this registry to retry an old provider credential under another grant ID.
CREATE TABLE hhc_query.refresh_identity (
 fingerprint bytea PRIMARY KEY CHECK(octet_length(fingerprint)=32),
 grant_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hhc_query.quota_limit (
 quota_id text NOT NULL, bucket text NOT NULL CHECK(bucket IN ('hhc.read','youtube.search','youtube.default')),
 period_seconds integer NOT NULL CHECK(period_seconds IN (60,3600,86400)),
 maximum bigint NOT NULL CHECK(maximum>0),
 reset_zone text GENERATED ALWAYS AS (CASE WHEN bucket IN ('youtube.search','youtube.default') THEN 'America/Los_Angeles' ELSE 'UTC' END) STORED,
 CHECK(bucket NOT IN ('youtube.search','youtube.default') OR period_seconds=86400), PRIMARY KEY(quota_id,bucket)
);
CREATE TABLE hhc_query.quota_usage (
 quota_key text NOT NULL, bucket text NOT NULL, window_start bigint NOT NULL,
 used bigint NOT NULL CHECK(used>=0), expires_at timestamptz NOT NULL,
 PRIMARY KEY(quota_key,bucket,window_start)
);
CREATE INDEX query_quota_expiry ON hhc_query.quota_usage(expires_at);
-- Owner maintenance may delete expired quota_usage rows. No application cron or
-- unbounded in-request sweep. Retain at least the longest configured window.
-- Runtime permission preview is ../permissions.sql.tmpl. It includes EXECUTE
-- on lock_policy(text) without policy UPDATE, narrow grant-state UPDATE,
-- and INSERT/SELECT-only refresh_identity tombstones. Authority writers follow
-- policy → grant → quota ordering. No request executes this migration.
