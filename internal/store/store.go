package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wallet-api/internal/wallet"
)

//go:embed schema.sql
var schemaSQL string

// Postgres implements wallet.TokenStore and wallet.TxCache over a pgx pool.
type Postgres struct {
	pool *pgxpool.Pool
}

// New opens a connection pool and verifies connectivity.
func New(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// Migrate applies the embedded schema (idempotent).
func (s *Postgres) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

// Close releases the pool.
func (s *Postgres) Close() { s.pool.Close() }

func tokenKey(t wallet.Token) string {
	if t.IsNative || t.TokenAddress == nil {
		return "native"
	}
	return strings.ToLower(*t.TokenAddress)
}

// SaveTokens replaces the token snapshot for (address, network) in one transaction.
func (s *Postgres) SaveTokens(ctx context.Context, p *wallet.TokenPortfolio) error {
	address := strings.ToLower(p.Address)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	// Roll back with a fresh context so a canceled/timed-out request ctx can't
	// abort the ROLLBACK and force the pool to discard the connection.
	defer tx.Rollback(context.Background())

	if _, err := tx.Exec(ctx, `DELETE FROM wallet_tokens WHERE address=$1 AND network=$2`, address, p.Network); err != nil {
		return err
	}
	for _, t := range p.Tokens {
		var curr, val, updated *string
		if t.Price != nil {
			curr, val, updated = &t.Price.Currency, &t.Price.Value, &t.Price.LastUpdatedAt
		}
		var tokenAddr *string
		if t.TokenAddress != nil {
			lower := strings.ToLower(*t.TokenAddress)
			tokenAddr = &lower
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO wallet_tokens
			(address, network, token_key, token_address, is_native, symbol, name, decimals,
			 raw_balance, balance, price_currency, price_value, price_updated_at, fetched_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			address, p.Network, tokenKey(t), tokenAddr, t.IsNative, t.Symbol, t.Name, t.Decimals,
			t.RawBalance, t.Balance, curr, val, updated, p.FetchedAt)
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO token_fetch_meta (address, network, fetched_at)
		VALUES ($1,$2,$3)
		ON CONFLICT (address, network) DO UPDATE SET fetched_at=EXCLUDED.fetched_at`,
		address, p.Network, p.FetchedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetFreshTokens returns the snapshot if its fetch time is within ttl.
func (s *Postgres) GetFreshTokens(ctx context.Context, address, network string, ttl time.Duration) (*wallet.TokenPortfolio, bool, error) {
	address = strings.ToLower(address)

	var fetchedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT fetched_at FROM token_fetch_meta
		WHERE address=$1 AND network=$2 AND fetched_at > now() - $3::interval`,
		address, network, intervalArg(ttl)).Scan(&fetchedAt)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT token_address, is_native, symbol, name, decimals, raw_balance, balance,
		       price_currency, price_value, price_updated_at
		FROM wallet_tokens WHERE address=$1 AND network=$2`, address, network)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	p := &wallet.TokenPortfolio{Address: address, Network: network, FetchedAt: fetchedAt}
	for rows.Next() {
		var t wallet.Token
		var curr, val, updated *string
		if err := rows.Scan(&t.TokenAddress, &t.IsNative, &t.Symbol, &t.Name, &t.Decimals,
			&t.RawBalance, &t.Balance, &curr, &val, &updated); err != nil {
			return nil, false, err
		}
		if curr != nil {
			t.Price = &wallet.Price{Currency: *curr, Value: derefStr(val), LastUpdatedAt: derefStr(updated)}
		}
		p.Tokens = append(p.Tokens, t)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return p, true, nil
}

// SaveTransactions upserts a transaction page as JSON.
func (s *Postgres) SaveTransactions(ctx context.Context, address, params string, page *wallet.TransactionPage) error {
	address = strings.ToLower(address)

	payload, err := json.Marshal(page)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO tx_cache (address, params, payload, fetched_at)
		VALUES ($1,$2,$3, now())
		ON CONFLICT (address, params) DO UPDATE SET payload=EXCLUDED.payload, fetched_at=now()`,
		address, params, payload)
	return err
}

// GetFreshTransactions returns the cached page if within ttl.
func (s *Postgres) GetFreshTransactions(ctx context.Context, address, params string, ttl time.Duration) (*wallet.TransactionPage, bool, error) {
	address = strings.ToLower(address)

	var payload []byte
	err := s.pool.QueryRow(ctx, `
		SELECT payload FROM tx_cache
		WHERE address=$1 AND params=$2 AND fetched_at > now() - $3::interval`,
		address, params, intervalArg(ttl)).Scan(&payload)
	if err == pgx.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var page wallet.TransactionPage
	if err := json.Unmarshal(payload, &page); err != nil {
		return nil, false, err
	}
	return &page, true, nil
}

func intervalArg(ttl time.Duration) string {
	return fmt.Sprintf("%d seconds", int64(ttl.Seconds()))
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
