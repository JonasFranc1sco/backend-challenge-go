-- Revert Migration 000004
DROP TABLE IF EXISTS outbox_events CASCADE;
DROP TABLE IF EXISTS inbox_messages CASCADE;
