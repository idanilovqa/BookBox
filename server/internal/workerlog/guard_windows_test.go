//go:build windows

package workerlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRejectsWindowsDirectoryReparsePoint(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "logs")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("creating Windows directory reparse point requires additional privilege: %v", err)
	}
	if writer, err := Open(link, 32, 2); err == nil {
		_ = writer.Close()
		t.Fatal("Open() accepted a directory reparse point")
	}
}
