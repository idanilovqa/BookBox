package catalog

import (
	"context"
	"testing"

	"bookbox.local/server/internal/database"
	"bookbox.local/server/internal/testpostgres"
)

func TestLoadGroupingScopeUsesAReadOnlyCompatibleCorrectionQuery(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []LibraryRoot{{
		ID: "audiobooks", DisplayName: "Audiobooks",
	}}); err != nil {
		t.Fatal(err)
	}

	files, corrections, err := repository.LoadGroupingScope(ctx, "audiobooks", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 || len(corrections) != 0 {
		t.Fatalf("scope = %d files, %d corrections; want empty", len(files), len(corrections))
	}
}
