//go:build windows

package main

import (
	"os"
	"path/filepath"
)

func defaultWorkerConfigPath() string {
	programData := os.Getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	return filepath.Join(programData, "BookBox", "catalog-worker.yml")
}
