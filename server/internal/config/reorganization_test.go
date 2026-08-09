package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLibraryReorganizationIsDisabledByDefaultAndMustBeExplicitlyEnabled(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		setting string
		want    bool
	}{
		{name: "omitted", want: false},
		{name: "disabled", setting: "allow_library_reorganization: false\n", want: false},
		{name: "enabled", setting: "allow_library_reorganization: true\n", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "bookbox.yaml")
			contents := fmt.Sprintf("%sroots:\n  - id: audio\n    path: %q\n",
				test.setting, t.TempDir())
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}

			got, err := LoadWorker(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.AllowLibraryReorganization != test.want {
				t.Fatalf("AllowLibraryReorganization = %t, want %t", got.AllowLibraryReorganization, test.want)
			}
		})
	}
}
