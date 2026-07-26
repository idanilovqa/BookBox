package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRequiresUniqueSafeRootIDs(t *testing.T) {
	path := writeConfig(t, `
listen: ":8443"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/a}
  - {id: audiobooks, name: Duplicate, container_path: /library/b}
`)

	_, err := Load(path)
	if !errors.Is(err, ErrDuplicateRootID) {
		t.Fatalf("Load() error = %v, want ErrDuplicateRootID", err)
	}
}

func TestLoadAcceptsAudiobookRoot(t *testing.T) {
	path := writeConfig(t, `
listen: ":8443"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`)

	cfg, err := Load(path)
	if err != nil || cfg.Roots[0].ID != "audiobooks" {
		t.Fatalf("Load() = %#v, %v", cfg, err)
	}
}

func TestLoadRejectsUnsafeRootID(t *testing.T) {
	path := writeConfig(t, `
listen: ":8443"
roots:
  - {id: ../audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`)

	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted an unsafe root ID")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, `
listen: ":8443"
unexpected: true
roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`)

	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted an unknown field")
	}
}

func TestLoadRequiresListenAndRoots(t *testing.T) {
	for name, contents := range map[string]string{
		"listen": `roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`,
		"roots": `listen: ":8443"
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, contents)); err == nil {
				t.Fatalf("Load() accepted configuration missing %s", name)
			}
		})
	}
}

func TestLoadRequiresAbsoluteContainerPath(t *testing.T) {
	path := writeConfig(t, `
listen: ":8443"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: library/audiobooks}
`)

	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted a relative container path")
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
