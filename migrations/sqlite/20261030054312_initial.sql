-- +goose Up
CREATE TABLE secrets (
    id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('active','used','expired')),
    used_at INTEGER,
    terminal_at INTEGER,
    tracking_hash BLOB NOT NULL CHECK(length(tracking_hash) = 32),
    claim_hash BLOB,
    claim_until INTEGER,
    claim_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX secrets_expiry ON secrets(expires_at) WHERE status = 'active';
CREATE INDEX secrets_terminal ON secrets(terminal_at) WHERE status <> 'active';
CREATE TABLE payloads (
    id TEXT PRIMARY KEY REFERENCES secrets(id) ON DELETE CASCADE,
    ciphertext TEXT NOT NULL,
    iv TEXT NOT NULL,
    proof_hash BLOB NOT NULL CHECK(length(proof_hash) = 32)
);
CREATE TABLE store_counters (
    singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
    created INTEGER NOT NULL DEFAULT 0,
    claims INTEGER NOT NULL DEFAULT 0,
    consumed INTEGER NOT NULL DEFAULT 0,
    expired INTEGER NOT NULL DEFAULT 0,
    purge_generation INTEGER NOT NULL DEFAULT 0,
    checkpoint_generation INTEGER NOT NULL DEFAULT 0
);
INSERT INTO store_counters(singleton) VALUES(1);

-- +goose Down
DROP TABLE payloads;
DROP TABLE secrets;
DROP TABLE store_counters;
