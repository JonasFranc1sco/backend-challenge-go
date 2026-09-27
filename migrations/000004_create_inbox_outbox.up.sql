-- Migration 000004: Create Inbox and Outbox Tables
CREATE TABLE IF NOT EXISTS inbox_messages (
    consumer_name VARCHAR(64) NOT NULL,
    message_id VARCHAR(128) NOT NULL,
    message_hash CHAR(64) NOT NULL,
    status VARCHAR(24) NOT NULL CHECK (status IN ('PROCESSING', 'PROCESSED', 'FAILED')),
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE IF NOT EXISTS outbox_events (
    id VARCHAR(64) PRIMARY KEY,
    event_type VARCHAR(64) NOT NULL,
    aggregate_type VARCHAR(64) NOT NULL,
    aggregate_id VARCHAR(128) NOT NULL,
    correlation_id VARCHAR(128) NOT NULL,
    causation_id VARCHAR(128),
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    version INT NOT NULL DEFAULT 1,
    attempts INT NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ,
    last_error TEXT
);

-- Index for concurrent polling via FOR UPDATE SKIP LOCKED
CREATE INDEX IF NOT EXISTS idx_outbox_pending
    ON outbox_events (next_retry_at, occurred_at ASC)
    WHERE published_at IS NULL;
