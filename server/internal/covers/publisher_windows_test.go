package covers

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRootAllowsDifferentWindowsVolume(t *testing.T) {
	backup := t.TempDir()
	source := `Z:\Audiobooks`
	if strings.EqualFold(filepath.VolumeName(backup), `Z:`) {
		source = `Y:\Audiobooks`
	}
	// The caller supplies a resolver-canonicalized source root. Different
	// canonical volumes cannot be descendants of one another.
	if err := validateBackupRoot(backup, source); err != nil {
		t.Fatalf("backup on separate volume rejected: %v", err)
	}
}
