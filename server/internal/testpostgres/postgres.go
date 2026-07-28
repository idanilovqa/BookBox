package testpostgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const DSNEnvironment = "BOOKBOX_TEST_POSTGRES_DSN"

func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(DSNEnvironment)
	if dsn == "" {
		t.Skipf("%s is not set", DSNEnvironment)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	adminConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL test DSN: %v", err)
	}
	databaseName := "bookbox_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("open PostgreSQL admin pool: %v", err)
	}
	if _, err := admin.Exec(ctx, "create database "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		admin.Close()
		t.Fatalf("create PostgreSQL test database: %v", err)
	}

	testConfig := adminConfig.Copy()
	testConfig.ConnConfig.Database = databaseName
	pool, err := pgxpool.NewWithConfig(ctx, testConfig)
	if err != nil {
		dropDatabase(t, admin, databaseName)
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		dropDatabase(t, admin, databaseName)
		t.Fatalf("ping PostgreSQL test database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropDatabase(t, admin, databaseName)
		admin.Close()
	})
	return pool
}

func dropDatabase(t *testing.T, admin *pgxpool.Pool, databaseName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := admin.Exec(
		ctx,
		"drop database if exists "+pgx.Identifier{databaseName}.Sanitize()+" with (force)",
	); err != nil {
		t.Errorf("drop PostgreSQL test database %s: %v", databaseName, err)
	}
}
