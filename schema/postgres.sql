-- Shadowcut source schema (PostgreSQL 16).
-- This is written the way a typical Postgres app would write it -- including the
-- habits that become hazards on Spanner (bigserial keys, numeric(p,s), uuid, ...).
-- The hazard scanner reads this file.

CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

-- Live Wikipedia recent changes (Wikimedia EventStreams).
CREATE TABLE edits (
    id           bigserial PRIMARY KEY,
    event_id     uuid        NOT NULL UNIQUE,
    wiki         varchar(64) NOT NULL,
    page_title   text        NOT NULL,
    namespace    bigint      NOT NULL,
    user_name    text,                       -- NULL for anonymous / temporary editors
    is_bot       boolean     NOT NULL,
    is_minor     boolean,                    -- NULL for non-edit change types
    change_type  varchar(16) NOT NULL,
    comment      text,
    old_len      bigint,
    new_len      bigint,
    size_delta   bigint,
    growth_ratio double precision,           -- new_len / old_len
    rev_new      bigint,
    edit_ts      timestamptz NOT NULL,       -- millisecond precision from meta.dt
    ingested_at  timestamptz NOT NULL DEFAULT now()  -- microsecond precision
);
CREATE INDEX edits_user_ts ON edits (user_name, edit_ts DESC);
CREATE INDEX edits_ts ON edits (edit_ts);
CREATE INDEX edits_title ON edits (page_title);

-- Per-page aggregate, upserted on every edit (generates UPDATE change events).
CREATE TABLE pages (
    wiki         varchar(64) NOT NULL,
    title        text        NOT NULL,
    edit_count   bigint      NOT NULL DEFAULT 0,
    bytes        bigint,
    avg_delta    numeric(14,4),               -- running mean of size_delta
    last_editor  text,
    last_edit_ts timestamptz NOT NULL,
    PRIMARY KEY (wiki, title)
);

-- GH Archive events (secondary dataset, different shape: string ids, jsonb).
CREATE TABLE gh_events (
    id          bigint PRIMARY KEY,
    type        varchar(64) NOT NULL,
    actor_login text        NOT NULL,
    repo_name   text        NOT NULL,
    org_login   text,
    is_public   boolean     NOT NULL,
    created_at  timestamptz NOT NULL,
    payload     jsonb
);
CREATE INDEX gh_events_repo ON gh_events (repo_name, created_at);

-- Replication heartbeat, updated by Debezium's heartbeat.action.query.
-- Lag = (heartbeat ts in Postgres) - (heartbeat ts that reached Spanner).
CREATE TABLE sc_heartbeat (
    id integer PRIMARY KEY,
    ts timestamptz NOT NULL
);
INSERT INTO sc_heartbeat VALUES (1, now());

-- Probe table for fault-injection experiments: every row is written once with a
-- unique (writer, seq); lost / duplicated writes are measured against it.
CREATE TABLE probe_events (
    writer   varchar(32) NOT NULL,
    seq      bigint      NOT NULL,
    payload  text        NOT NULL,
    written_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (writer, seq)
);

-- Publication for Debezium (pgoutput). Control tables are deliberately excluded.
CREATE PUBLICATION shadowcut_pub FOR TABLE edits, pages, gh_events, sc_heartbeat, probe_events;

-- Shadowcut control plane (NOT replicated).
CREATE SCHEMA shadowcut;
CREATE TABLE shadowcut.control (
    id         integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    mode       text NOT NULL DEFAULT 'postgres',   -- postgres | frozen | spanner
    step       text NOT NULL DEFAULT 'idle',       -- cutover/rollback state machine step
    updated_at timestamptz NOT NULL DEFAULT now(),
    note       text
);
INSERT INTO shadowcut.control (id) VALUES (1);
