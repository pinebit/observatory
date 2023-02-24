package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

type Database interface {
	Output

	Connect(ctx context.Context, url string) error
	Close(ctx context.Context) error
	MigrateSchema(ctx context.Context, rpcs []Chain) error
}

type database struct {
	db     *sqlx.DB
	logger *zap.SugaredLogger
}

var (
	errDatabaseClosed = errors.New("database is closed")
)

func NewDatabase(logger *zap.SugaredLogger) Database {
	return &database{
		logger: logger,
	}
}

func (d *database) Connect(ctx context.Context, url string) error {
	db, err := sqlx.Open("postgres", url)
	if err != nil {
		return err
	}
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	d.db = db
	return nil
}

func (d *database) Close(ctx context.Context) error {
	if d.db != nil {
		if err := d.db.Close(); err != nil {
			return err
		}
		d.db = nil
	}
	return nil
}

func (d database) MigrateSchema(ctx context.Context, chains []Chain) error {
	if d.db == nil {
		return errDatabaseClosed
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	for _, chain := range chains {
		_, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+chain.Name())
		if err != nil {
			d.logger.Errorw("DB failed to create schema", "name", chain.Name(), "err", err)
			defer tx.Rollback()
			return err
		}

		for _, contract := range chain.Contracts() {
			columns := `id BIGSERIAL PRIMARY KEY,
						block_ts TIMESTAMP NOT NULL,
						tx_hash TEXT NOT NULL,
						address TEXT NOT NULL,
						removed BOOL NOT NULL,
						event TEXT NOT NULL,
						args JSONB NOT NULL`
			q := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s);", eventsTableQN(contract), columns)
			_, err := tx.ExecContext(ctx, q)
			if err != nil {
				d.logger.Errorw("DB failed to create table", "err", err, "q", q)
				defer tx.Rollback()
				return err
			}
		}
	}

	return tx.Commit()
}

func (d database) Write(ctx context.Context, log types.Log, contract Contract, event string, args map[string]interface{}) {
	jsonb, err := json.Marshal(args)
	if err != nil {
		d.logger.Errorw("DB failed marshal jsonb", "err", err)
	} else {
		q := fmt.Sprintf("INSERT INTO %s (block_ts, tx_hash, address, removed, event, args) VALUES (now(), $1, $2, $3, $4, $5)", eventsTableQN(contract))
		_, err = d.db.ExecContext(ctx, q, log.TxHash.Hex(), log.Address.Hex(), log.Removed, event, jsonb)
		if err != nil {
			d.logger.Errorw("DB failed to insert", "err", err, "q", q)
		}
	}
}

func eventsTableQN(contract Contract) string {
	return fmt.Sprintf("%s.%s_events", contract.Chain(), contract.Name())
}
