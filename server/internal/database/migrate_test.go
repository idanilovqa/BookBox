package database

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"netstorage.local/server/internal/testpostgres"
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
	if applied != 1 {
		t.Fatalf("schema migration rows = %d, want 1", applied)
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
