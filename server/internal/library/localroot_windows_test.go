//go:build windows

package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestValidateLocalRootClassifiesWindowsDriveTypes(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name      string
		driveType uint32
		wantErr   error
	}{
		{name: "fixed", driveType: windows.DRIVE_FIXED},
		{name: "removable", driveType: windows.DRIVE_REMOVABLE},
		{name: "remote", driveType: windows.DRIVE_REMOTE, wantErr: ErrUnsupportedRoot},
		{name: "CD-ROM", driveType: windows.DRIVE_CDROM, wantErr: ErrUnsupportedRoot},
		{name: "RAM disk", driveType: windows.DRIVE_RAMDISK, wantErr: ErrUnsupportedRoot},
		{name: "unknown", driveType: windows.DRIVE_UNKNOWN, wantErr: ErrUnsupportedRoot},
		{name: "missing drive", driveType: windows.DRIVE_NO_ROOT_DIR, wantErr: ErrUnsupportedRoot},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := getDriveType
			getDriveType = func(string) uint32 { return test.driveType }
			t.Cleanup(func() { getDriveType = original })

			err := ValidateLocalRoot(root)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateLocalRoot() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateLocalRootRejectsLexicalAndResolvedUNC(t *testing.T) {
	if err := ValidateLocalRoot(`\\server\share\library`); !errors.Is(err, ErrUnsupportedRoot) {
		t.Fatalf("lexical UNC error = %v, want ErrUnsupportedRoot", err)
	}
	if err := validatePlatformLocalRoot(`\\server\share\library`); !errors.Is(err, ErrUnsupportedRoot) {
		t.Fatalf("resolved UNC error = %v, want ErrUnsupportedRoot", err)
	}
}

func TestValidateLocalRootRejectsUnavailableAndNonDirectoryWithoutLeakingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "private-library", "missing")
	err := ValidateLocalRoot(missing)
	if !errors.Is(err, ErrRootUnavailable) {
		t.Fatalf("missing root error = %v, want ErrRootUnavailable", err)
	}
	if strings.Contains(err.Error(), missing) {
		t.Fatalf("missing root error leaked path: %v", err)
	}

	file := filepath.Join(t.TempDir(), "library.txt")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLocalRoot(file); !errors.Is(err, ErrUnsupportedRoot) {
		t.Fatalf("file root error = %v, want ErrUnsupportedRoot", err)
	}
	if err := ValidateLocalRoot(`relative\library`); !errors.Is(err, ErrUnsupportedRoot) {
		t.Fatalf("relative root error = %v, want ErrUnsupportedRoot", err)
	}
}
