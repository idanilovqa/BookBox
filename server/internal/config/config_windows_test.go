//go:build windows

package config

import (
	"path/filepath"
	"testing"
)

func TestLoadWorkerAcceptsAbsoluteWindowsPaths(t *testing.T) {
	path := writeConfigRaw(t, `roots:
  - {id: audiobooks, name: Audiobooks, path: 'H:\Audiobooks'}
worker:
  ffprobe_path: 'C:\Program Files\ffmpeg\bin\ffprobe.exe'
  cover_backup_root: 'C:\ProgramData\BookBox\backups'
  log_directory: 'C:\ProgramData\BookBox\logs'
  google_books_api_key_file: 'C:\ProgramData\BookBox\secrets\google-books-api-key'`)

	if _, err := LoadWorker(path); err != nil {
		t.Fatalf("LoadWorker() rejected absolute Windows paths: %v", err)
	}
}

func TestLoadWorkerRejectsRelativeWindowsRootPath(t *testing.T) {
	path := writeConfigRaw(t, `roots:
  - {id: audiobooks, name: Audiobooks, path: 'Library\Audiobooks'}`)

	if _, err := LoadWorker(path); err == nil {
		t.Fatal("LoadWorker() accepted a relative Windows root path")
	}
}

func TestWindowsWorkerExampleLoads(t *testing.T) {
	path := filepath.Join("..", "..", "..", "deploy", "windows", "catalog-worker.example.yml")
	if _, err := LoadWorker(path); err != nil {
		t.Fatalf("LoadWorker(catalog-worker.example.yml) error = %v", err)
	}
}
