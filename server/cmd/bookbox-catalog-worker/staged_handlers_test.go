package main

import (
	"os"
	"strings"
	"testing"
)

func TestWorkerCommandWiresEveryStagedCatalogHandler(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"scanner.NewInventory(",
		"catalog.JobScanRoot:",
		"catalog.JobProbeFile:",
		"catalog.JobInspectCompanion:",
		"catalog.JobRegroupScope:",
		"catalog.JobPublishSearch:",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("worker command does not wire %q", required)
		}
	}
	if strings.Contains(source, "Scanner: scanner.New(") {
		t.Error("scan_root still constructs the probing scanner instead of fast inventory")
	}
}
