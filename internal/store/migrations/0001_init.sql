CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE projects (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT        NOT NULL UNIQUE,
    source_locale   TEXT        NOT NULL,
    target_locale   TEXT        NOT NULL,
    -- Scales the built-in character expansion allowance. 1.0 is the default,
    -- higher is more permissive, and 0 turns the length check off.
    length_tolerance REAL       NOT NULL DEFAULT 1.0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE files (
    id          BIGSERIAL PRIMARY KEY,
    project_id  BIGINT      NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    format      TEXT        NOT NULL CHECK (format IN ('xliff', 'qtts')),
    -- The exact bytes that were uploaded. Export re-splices these, which is how
    -- entries nobody edited come back out unchanged.
    original    BYTEA       NOT NULL,
    sha256      TEXT        NOT NULL,
    imported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE TYPE segment_status AS ENUM ('untranslated', 'draft', 'needs_review', 'approved', 'rejected');

-- Where the current text came from. 'machine' is an LLM first pass, 'memory' is
-- a translation memory hit a user accepted, 'human' is typed by a person.
CREATE TYPE segment_origin AS ENUM ('import', 'human', 'machine', 'memory');

CREATE TABLE segments (
    id          BIGSERIAL PRIMARY KEY,
    file_id     BIGINT         NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    unit_key    TEXT           NOT NULL,
    -- Qt plural messages hold several forms under one key; everything else is 0.
    form_index  INT            NOT NULL DEFAULT 0,
    context     TEXT           NOT NULL DEFAULT '',
    source_text TEXT           NOT NULL,
    target_text TEXT           NOT NULL DEFAULT '',
    status      segment_status NOT NULL DEFAULT 'untranslated',
    origin      segment_origin NOT NULL DEFAULT 'import',
    is_plural   BOOLEAN        NOT NULL DEFAULT FALSE,
    max_width   INT            NOT NULL DEFAULT 0,
    notes       TEXT           NOT NULL DEFAULT '',
    reviewed_by TEXT,
    reviewed_at TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ    NOT NULL DEFAULT now(),

    UNIQUE (file_id, unit_key, form_index),

    -- Nothing reaches "approved" without a named human attached to it. This is
    -- the database-level guarantee that an LLM suggestion cannot be stored as a
    -- reviewed translation, whatever the application code does.
    CONSTRAINT approved_needs_a_reviewer
        CHECK (status <> 'approved' OR (reviewed_by IS NOT NULL AND reviewed_by <> '')),
    CONSTRAINT reviewer_implies_timestamp
        CHECK ((reviewed_by IS NULL) = (reviewed_at IS NULL))
);

CREATE INDEX segments_file_status ON segments (file_id, status);
CREATE INDEX segments_source ON segments (source_text);

CREATE TABLE qa_issues (
    id         BIGSERIAL PRIMARY KEY,
    segment_id BIGINT NOT NULL REFERENCES segments(id) ON DELETE CASCADE,
    kind       TEXT   NOT NULL,
    severity   TEXT   NOT NULL CHECK (severity IN ('error', 'warning')),
    message    TEXT   NOT NULL
);

CREATE INDEX qa_issues_segment ON qa_issues (segment_id);

CREATE TABLE glossary_terms (
    id             BIGSERIAL PRIMARY KEY,
    project_id     BIGINT  NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_term    TEXT    NOT NULL,
    target_term    TEXT    NOT NULL,
    case_sensitive BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (project_id, source_term)
);

-- The translation memory. Only translations a human approved are written here,
-- so suggestions can never be sourced from unreviewed machine output.
CREATE TABLE tm_entries (
    id            BIGSERIAL PRIMARY KEY,
    project_id    BIGINT      NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_locale TEXT        NOT NULL,
    target_locale TEXT        NOT NULL,
    -- Plain text with markup stripped: what gets embedded and matched on.
    source_text   TEXT        NOT NULL,
    -- The translation as it is stored in the file, inline tags included.
    target_text   TEXT        NOT NULL,
    -- sha256 of the normalised source, for exact lookups without a scan.
    source_hash   TEXT        NOT NULL,
    embedding     vector(384) NOT NULL,
    segment_id    BIGINT      REFERENCES segments(id) ON DELETE SET NULL,
    approved_by   TEXT        NOT NULL CHECK (approved_by <> ''),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (project_id, target_locale, source_hash, target_text)
);

CREATE INDEX tm_entries_exact ON tm_entries (project_id, target_locale, source_hash);

-- HNSW over cosine distance: the query is ORDER BY embedding <=> $1.
CREATE INDEX tm_entries_embedding ON tm_entries
    USING hnsw (embedding vector_cosine_ops);
