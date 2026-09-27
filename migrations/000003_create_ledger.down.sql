-- Revert Migration 000003
DROP TRIGGER IF EXISTS trg_ledger_immutable ON wallet_ledger_entries;
DROP FUNCTION IF EXISTS trg_prevent_ledger_mutation();
DROP TABLE IF EXISTS wallet_ledger_entries CASCADE;
