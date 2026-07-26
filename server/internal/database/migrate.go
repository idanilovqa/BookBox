package database

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"netstorage.local/server/migrations"
)

const migrationAdvisoryLock int64 = 0x4e53544f52414745

var migrationName = regexp.MustCompile(`^([0-9]+)_[a-z0-9_]+\.sql$`)

type migration struct {
	version int64
	name    string
	sql     string
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("database pool is required")
	}
	files, err := loadMigrations()
	if err != nil {
		return err
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Release()

	if _, err := connection.Exec(ctx, "select pg_advisory_lock($1)", migrationAdvisoryLock); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		_, _ = connection.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", migrationAdvisoryLock)
	}()

	for _, file := range files {
		transaction, err := connection.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", file.name, err)
		}
		if _, err := transaction.Exec(
			ctx,
			`create table if not exists schema_migrations (
				version bigint primary key,
				applied_at timestamptz not null default now()
			)`,
		); err != nil {
			_ = transaction.Rollback(ctx)
			return fmt.Errorf("prepare migration ledger for %s: %w", file.name, err)
		}
		var applied bool
		if err := transaction.QueryRow(
			ctx,
			`select exists(select 1 from schema_migrations where version=$1)`,
			file.version,
		).Scan(&applied); err != nil {
			_ = transaction.Rollback(ctx)
			return fmt.Errorf("check migration %s: %w", file.name, err)
		}
		if !applied {
			if _, err := transaction.Exec(ctx, file.sql); err != nil {
				_ = transaction.Rollback(ctx)
				return fmt.Errorf("apply migration %s: %w", file.name, err)
			}
			if _, err := transaction.Exec(
				ctx,
				`insert into schema_migrations(version) values ($1)`,
				file.version,
			); err != nil {
				_ = transaction.Rollback(ctx)
				return fmt.Errorf("record migration %s: %w", file.name, err)
			}
		}
		if err := transaction.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", file.name, err)
		}
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	files := make([]migration, 0, len(entries))
	versions := make(map[int64]string)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		match := migrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		if previous, exists := versions[version]; exists {
			return nil, fmt.Errorf(
				"duplicate migration version %d in %q and %q",
				version,
				previous,
				entry.Name(),
			)
		}
		contents, err := fs.ReadFile(migrations.Files, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		versions[version] = entry.Name()
		files = append(files, migration{
			version: version,
			name:    entry.Name(),
			sql:     string(contents),
		})
	}
	sort.Slice(files, func(left, right int) bool {
		return files[left].version < files[right].version
	})
	if len(files) == 0 {
		return nil, fmt.Errorf("no embedded migrations")
	}
	return files, nil
}
