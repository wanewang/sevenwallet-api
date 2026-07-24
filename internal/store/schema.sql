CREATE TABLE IF NOT EXISTS wallet_tokens (
    address          TEXT NOT NULL,
    network          TEXT NOT NULL,
    token_key        TEXT NOT NULL,
    token_address    TEXT,
    is_native        BOOLEAN NOT NULL,
    symbol           TEXT,
    name             TEXT,
    decimals         INTEGER,
    raw_balance      TEXT NOT NULL,
    balance          TEXT NOT NULL,
    price_currency   TEXT,
    price_value      TEXT,
    price_updated_at TEXT,
    fetched_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (address, network, token_key)
);

CREATE TABLE IF NOT EXISTS token_fetch_meta (
    address    TEXT NOT NULL,
    network    TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (address, network)
);

CREATE TABLE IF NOT EXISTS tx_cache (
    address    TEXT NOT NULL,
    params     TEXT NOT NULL DEFAULT '',
    payload    JSONB NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (address, params)
);

CREATE TABLE IF NOT EXISTS lifi_token_lists (
    chain      TEXT PRIMARY KEY,
    payload    JSONB       NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS token_metadata (
    chain         TEXT NOT NULL,
    token_address TEXT NOT NULL,
    possible_spam BOOLEAN NOT NULL,
    verified      BOOLEAN NOT NULL,
    symbol        TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL DEFAULT '',
    logo          TEXT NOT NULL DEFAULT '',
    decimals      INTEGER NOT NULL DEFAULT 0,
    fetched_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (chain, token_address)
);

CREATE TABLE IF NOT EXISTS coingecko_coin_mappings (
    id         TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    symbol     TEXT        NOT NULL,
    chain      TEXT        NOT NULL,
    address    TEXT        NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (id, chain, address)
);

CREATE INDEX IF NOT EXISTS coingecko_coin_mappings_lookup_idx
    ON coingecko_coin_mappings (chain, address);

CREATE TABLE IF NOT EXISTS coingecko_market_data (
    chain                TEXT        NOT NULL,
    token_key            TEXT        NOT NULL,
    coingecko_id         TEXT        NOT NULL,
    price_usd            TEXT,
    change_24h_percent   NUMERIC,
    market_cap_usd       NUMERIC,
    market_updated_at    TIMESTAMPTZ,
    fetched_at           TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (chain, token_key)
);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'coingecko_market_data'
          AND column_name = 'price_usd'
          AND data_type = 'numeric'
    ) THEN
        ALTER TABLE coingecko_market_data
            ALTER COLUMN price_usd TYPE TEXT USING price_usd::text;
    END IF;
END $$;
