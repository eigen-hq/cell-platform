// Package store keeps the records of the platform database (E-98) that
// are not desired state: today the items of task transcripts. Only the
// platform API reaches it.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Item is one item of a task's transcript at its position.
type Item struct {
	Seq  int             `json:"seq"`
	At   time.Time       `json:"at"`
	Item json.RawMessage `json:"item"`
}

// ErrGap is an append that would leave a hole in a transcript.
var ErrGap = errors.New("gap in the transcript")

// DB is the platform database.
type DB struct {
	pool  *pgxpool.Pool
	mu    sync.Mutex
	ready bool
}

// Open connects lazily: the API starts while the database is still coming
// up, and a call that needs it fails until it is there.
func Open(ctx context.Context, uri string) (*DB, error) {
	pool, err := pgxpool.New(ctx, uri)
	if err != nil {
		return nil, err
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close() { d.pool.Close() }

const schema = `create table if not exists task_items (
	task text not null,
	seq integer not null,
	item jsonb not null,
	at timestamptz not null default now(),
	primary key (task, seq))`

// migrate creates the tables on first use; the lock keeps two API
// processes from creating them at once.
func (d *DB) migrate(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ready {
		return nil
	}
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(1, 0)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
	if err != nil {
		return fmt.Errorf("platform database: %w", err)
	}
	d.ready = true
	return nil
}

// Append stores items at positions from on in the transcript of task (its
// UID). A position it holds already keeps its item, so a repeated call
// changes nothing; a from past the end is ErrGap.
func (d *DB) Append(ctx context.Context, task string, from int, items []json.RawMessage) error {
	if err := d.migrate(ctx); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(2, hashtext($1))", task); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, "select count(*) from task_items where task = $1", task).Scan(&n); err != nil {
			return err
		}
		if from > n {
			return fmt.Errorf("%w: the next position is %d", ErrGap, n)
		}
		for i, it := range items {
			if _, err := tx.Exec(ctx, "insert into task_items (task, seq, item) values ($1, $2, $3) on conflict do nothing", task, from+i, it); err != nil {
				return err
			}
		}
		return nil
	})
}

// Items is the transcript of task (its UID), in order.
func (d *DB) Items(ctx context.Context, task string) ([]Item, error) {
	if err := d.migrate(ctx); err != nil {
		return nil, err
	}
	rows, err := d.pool.Query(ctx, "select seq, at, item from task_items where task = $1 order by seq", task)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Item])
}

// Memory keeps transcripts in memory, for tests: what DB does, without a
// database.
type Memory struct {
	mu    sync.Mutex
	items map[string][]Item
}

func (m *Memory) Append(_ context.Context, task string, from int, items []json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	have := m.items[task]
	if from > len(have) {
		return fmt.Errorf("%w: the next position is %d", ErrGap, len(have))
	}
	for i, it := range items {
		if from+i >= len(have) {
			have = append(have, Item{Seq: from + i, At: time.Now().UTC(), Item: it})
		}
	}
	if m.items == nil {
		m.items = map[string][]Item{}
	}
	m.items[task] = have
	return nil
}

func (m *Memory) Items(_ context.Context, task string) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Item{}, m.items[task]...), nil
}
