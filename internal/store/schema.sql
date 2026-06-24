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
