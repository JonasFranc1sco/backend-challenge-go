-- Migration 000002: Create Wager Transactions Table
CREATE TABLE IF NOT EXISTS wager_transactions (
    id VARCHAR(64) PRIMARY KEY,
    origin VARCHAR(16) NOT NULL CHECK (origin IN ('INTERNAL', 'EXTERNAL')),
    provider_id VARCHAR(64),
    external_transaction_id VARCHAR(128),
    idempotency_key VARCHAR(256),
    payload_hash CHAR(64),
    wallet_id VARCHAR(64) NOT NULL REFERENCES wallets(id),
    player_id VARCHAR(64) NOT NULL,
    round_id VARCHAR(128),
    game_id VARCHAR(128),
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    amount BIGINT NOT NULL CHECK (amount >= 0),
    currency VARCHAR(3) NOT NULL,
    reference_external_transaction_id VARCHAR(128),
    reference_internal_transaction_id VARCHAR(64) REFERENCES wager_transactions(id),
    status VARCHAR(24) NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    failure_code VARCHAR(64),
    balance_snapshot BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Constraints distinguishing internal OPENING from external transactions
    CONSTRAINT chk_external_metadata CHECK (
        origin = 'INTERNAL' OR (
            provider_id IS NOT NULL AND
            external_transaction_id IS NOT NULL AND
            idempotency_key IS NOT NULL AND
            payload_hash IS NOT NULL AND
            round_id IS NOT NULL AND
            game_id IS NOT NULL
        )
    ),
    CONSTRAINT chk_origin_opening CHECK (
        (kind = 'OPENING' AND origin = 'INTERNAL') OR
        (kind != 'OPENING' AND origin = 'EXTERNAL')
    )
);

-- Unique constraints for external operations
CREATE UNIQUE INDEX IF NOT EXISTS uq_tx_provider_external_id
    ON wager_transactions (provider_id, external_transaction_id)
    WHERE origin = 'EXTERNAL';

CREATE UNIQUE INDEX IF NOT EXISTS uq_tx_provider_idempotency_key
    ON wager_transactions (provider_id, idempotency_key)
    WHERE origin = 'EXTERNAL';

-- Only one OPENING per wallet
CREATE UNIQUE INDEX IF NOT EXISTS uq_wallet_single_opening
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

-- Query optimization indexes
CREATE INDEX IF NOT EXISTS idx_tx_wallet_created
    ON wager_transactions (wallet_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_tx_status_pending_ref
    ON wager_transactions (status, created_at ASC)
    WHERE status = 'PENDING_REFERENCE';
