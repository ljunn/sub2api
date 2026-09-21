-- Managed upstream connections are independent of gateway account credentials.
-- The document contains only public configuration/catalogue; authentication is encrypted.
CREATE TABLE IF NOT EXISTS upstream_sites (
    id TEXT PRIMARY KEY,
    document JSONB NOT NULL,
    secret TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
