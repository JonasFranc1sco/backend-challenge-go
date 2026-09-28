-- Migration 000005: Add retry tracking columns for pending references
ALTER TABLE wager_transactions
    ADD COLUMN IF NOT EXISTS retry_count INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE INDEX IF NOT EXISTS idx_tx_pending_ref_next
    ON wager_transactions (next_retry_at, created_at ASC)
    WHERE status = 'PENDING_REFERENCE';
