package database

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"bookbox.local/server/internal/testpostgres"
)

func TestMigrateCreatesCatalogTablesOnce(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()

	var wait sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- Migrate(ctx, pool)
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, table := range []string{
		"library_roots",
		"scan_runs",
		"source_files",
		"books",
		"book_files",
		"grouping_corrections",
		"metadata_matches",
		"cover_events",
		"worker_jobs",
		"availability_events",
	} {
		var exists bool
		if err := pool.QueryRow(
			ctx,
			`select to_regclass('public.' || $1) is not null`,
			table,
		).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s exists=%v err=%v", table, exists, err)
		}
	}

	var applied int
	if err := pool.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Fatalf("schema migration rows = %d, want 2", applied)
	}

	var rejectsCrossRootScan bool
	if err := pool.QueryRow(ctx, `
		select exists (
			select 1
			from pg_constraint
			where conrelid='source_files'::regclass
			  and contype='f'
			  and pg_get_constraintdef(oid) like '%(scan_run_id, root_id)%'
		)
	`).Scan(&rejectsCrossRootScan); err != nil {
		t.Fatal(err)
	}
	if !rejectsCrossRootScan {
		t.Fatal("source_files lacks a composite scan/root foreign key")
	}

	if _, err := pool.Exec(ctx, `
		insert into library_roots(id, display_name)
		values ('one','One'), ('two','Two')
	`); err != nil {
		t.Fatal(err)
	}
	scanID := uuid.New()
	if _, err := pool.Exec(ctx, `
		insert into scan_runs(id, root_id, started_at, status)
		values ($1, 'one', $2, 'running')
	`, scanID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into source_files(
			id, root_id, relative_path, size, modified_at, scan_run_id
		)
		values ($1, 'two', 'Book/file.m4b', 1, $2, $3)
	`, uuid.New(), time.Now().UTC(), scanID); err == nil {
		t.Fatal("source_files accepted a scan run belonging to another root")
	}
}

func TestMigrateUpgradesVersionOneWorkerJobsForDeduplication(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		create table schema_migrations (
			version bigint primary key,
			applied_at timestamptz not null default now()
		);
		insert into schema_migrations(version) values (1);
		create table worker_jobs (
			id uuid primary key,
			kind text not null,
			payload jsonb not null default '{}'::jsonb,
			status text not null default 'queued',
			run_after timestamptz not null default now(),
			attempts integer not null default 0,
			max_attempts integer not null default 5,
			locked_at timestamptz,
			locked_by text,
			lease_until timestamptz,
			lease_token uuid,
			last_error text,
			created_at timestamptz not null default now(),
			updated_at timestamptz not null default now()
		);
	`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var columnExists bool
	if err := pool.QueryRow(ctx, `
		select exists (
			select 1
			from information_schema.columns
			where table_schema='public'
			  and table_name='worker_jobs'
			  and column_name='dedupe_key'
		)
	`).Scan(&columnExists); err != nil {
		t.Fatal(err)
	}
	var indexExists bool
	if err := pool.QueryRow(ctx, `
		select to_regclass('public.worker_jobs_active_dedupe') is not null
	`).Scan(&indexExists); err != nil {
		t.Fatal(err)
	}
	if !columnExists || !indexExists {
		t.Fatalf("dedupe upgrade column=%v index=%v", columnExists, indexExists)
	}
}
