-- Revert Migration 000005
DROP INDEX IF EXISTS idx_tx_pending_ref_next;
ALTER TABLE wager_transactions
    DROP COLUMN IF EXISTS retry_count,
    DROP COLUMN IF EXISTS next_retry_at;
