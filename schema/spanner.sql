-- Shadowcut target schema: Spanner, PostgreSQL interface (applied via PGAdapter).
-- This is the direct translation a migration tool would produce from
-- schema/postgres.sql (see docs/DECISIONS.md ADR-007):
--   bigserial      -> bigint (values come from the source via CDC)
--   numeric(14,4)  -> numeric        (type modifiers are rejected in Spanner DDL)
--   uuid           -> varchar(36)
-- Indexes are kept as-is so the scanner's hotspot findings remain visible.

ALTER DATABASE wiki SET spanner.default_sequence_kind = 'bit_reversed_positive';

CREATE TABLE edits (
    id           bigint      NOT NULL,
    event_id     varchar(36) NOT NULL,
    wiki         varchar(64) NOT NULL,
    page_title   text        NOT NULL,
    namespace    bigint      NOT NULL,
    user_name    text,
    is_bot       boolean     NOT NULL,
    is_minor     boolean,
    change_type  varchar(16) NOT NULL,
    comment      text,
    old_len      bigint,
    new_len      bigint,
    size_delta   bigint,
    growth_ratio double precision,
    rev_new      bigint,
    edit_ts      timestamptz NOT NULL,
    ingested_at  timestamptz NOT NULL,
    PRIMARY KEY (id)
);
CREATE UNIQUE INDEX edits_event_id ON edits (event_id);
CREATE INDEX edits_user_ts ON edits (user_name, edit_ts DESC);
CREATE INDEX edits_ts ON edits (edit_ts);
CREATE INDEX edits_title ON edits (page_title);

-- Used by the demo app for new rows written after cutover (Spanner mode).
CREATE SEQUENCE edits_id_seq bit_reversed_positive;

CREATE TABLE pages (
    wiki         varchar(64) NOT NULL,
    title        text        NOT NULL,
    edit_count   bigint      NOT NULL,
    bytes        bigint,
    avg_delta    numeric,
    last_editor  text,
    last_edit_ts timestamptz NOT NULL,
    PRIMARY KEY (wiki, title)
);

CREATE TABLE gh_events (
    id          bigint      NOT NULL,
    type        varchar(64) NOT NULL,
    actor_login text        NOT NULL,
    repo_name   text        NOT NULL,
    org_login   text,
    is_public   boolean     NOT NULL,
    created_at  timestamptz NOT NULL,
    payload     jsonb,
    PRIMARY KEY (id)
);
CREATE INDEX gh_events_repo ON gh_events (repo_name, created_at);

CREATE TABLE sc_heartbeat (
    id bigint NOT NULL,
    ts timestamptz NOT NULL,
    PRIMARY KEY (id)
);

CREATE TABLE probe_events (
    writer     varchar(32) NOT NULL,
    seq        bigint      NOT NULL,
    payload    text        NOT NULL,
    written_at timestamptz NOT NULL,
    PRIMARY KEY (writer, seq)
);

-- ---- Shadowcut internal tables -------------------------------------------
-- (Spanner forbids names starting with "_", hence the sc_ prefix.)
-- Exactly-once guard: last applied Kafka offset per (topic, partition),
-- updated in the same transaction as the data mutations.
CREATE TABLE sc_offsets (
    topic      varchar(255) NOT NULL,
    kpartition bigint       NOT NULL,
    koffset    bigint       NOT NULL,
    source_ts  timestamptz,
    applied_at timestamptz  NOT NULL,
    PRIMARY KEY (topic, kpartition)
);

-- Optional audit (applier --audit): one row per applied Kafka record. times > 1
-- means the record's effect was applied more than once (a duplicated write).
CREATE TABLE sc_apply_audit (
    topic      varchar(255) NOT NULL,
    kpartition bigint       NOT NULL,
    koffset    bigint       NOT NULL,
    times      bigint       NOT NULL,
    PRIMARY KEY (topic, kpartition, koffset)
);

-- Reverse journal: every write the app makes to Spanner after cutover is
-- journaled in the same transaction, so rollback can replay it into Postgres.
CREATE TABLE sc_reverse_journal (
    id         varchar(36)  NOT NULL,
    tbl        varchar(64)  NOT NULL,
    row_data   jsonb        NOT NULL,
    created_at spanner.commit_timestamp NOT NULL,
    PRIMARY KEY (id)
);
