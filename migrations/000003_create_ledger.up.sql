-- Migration 000003: Create Wallet Ledger Entries Table with Immutability Protection
CREATE TABLE IF NOT EXISTS wallet_ledger_entries (
    id VARCHAR(64) PRIMARY KEY,
    wallet_id VARCHAR(64) NOT NULL REFERENCES wallets(id),
    transaction_id VARCHAR(64) NOT NULL REFERENCES wager_transactions(id),
    direction VARCHAR(8) NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount BIGINT NOT NULL CHECK (amount > 0),
    currency VARCHAR(3) NOT NULL,
    balance_before BIGINT NOT NULL CHECK (balance_before >= 0),
    balance_after BIGINT NOT NULL CHECK (balance_after >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Invariants: One entry per (wallet, tx) and strict balance arithmetic
    CONSTRAINT uq_ledger_wallet_tx UNIQUE (wallet_id, transaction_id),
    CONSTRAINT chk_ledger_math CHECK (
        (direction = 'CREDIT' AND balance_after = balance_before + amount) OR
        (direction = 'DEBIT'  AND balance_after = balance_before - amount)
    )
);

-- Index for stable chronological pagination & reconciliation
CREATE INDEX IF NOT EXISTS idx_ledger_wallet_created_id
    ON wallet_ledger_entries (wallet_id, created_at ASC, id ASC);

-- Trigger Function: Prohibit UPDATE or DELETE to enforce strict append-only ledger
CREATE OR REPLACE FUNCTION trg_prevent_ledger_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'Wallet ledger entries are append-only and strictly immutable. Modification or deletion is prohibited.';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_ledger_immutable ON wallet_ledger_entries;
CREATE TRIGGER trg_ledger_immutable
BEFORE UPDATE OR DELETE ON wallet_ledger_entries
FOR EACH ROW EXECUTE FUNCTION trg_prevent_ledger_mutation();
