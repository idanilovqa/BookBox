package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseFFProbeExtractsAudiobookFields(t *testing.T) {
	got, err := parseFFProbe([]byte(`{
		"format": {
			"duration": "3600.5",
			"tags": {
				"title": "Dune",
				"artist": "Frank Herbert",
				"album": "Dune",
				"genre": "Science Fiction",
				"date": "1965",
				"narrator": "Simon Vance"
			}
		},
		"streams": [{"codec_type": "audio", "codec_name": "aac"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Dune" || got.Duration != 3600500*time.Millisecond ||
		got.Narrator != "Simon Vance" || got.Author != "Frank Herbert" ||
		got.AudioCodec != "aac" {
		t.Fatalf("parsed metadata = %#v", got)
	}
}

func TestParseFFProbeRejectsMediaWithoutAudio(t *testing.T) {
	_, err := parseFFProbe([]byte(`{
		"format": {"duration": "1"},
		"streams": [{"codec_type": "video", "codec_name": "mjpeg", "width": 800, "height": 800}]
	}`))
	if !errors.Is(err, ErrUnsupportedMedia) {
		t.Fatalf("error = %v, want ErrUnsupportedMedia", err)
	}
}

func TestFFProbeUsesExactBoundedCommand(t *testing.T) {
	requirePOSIX(t)
	mediaPath := filepath.Join(t.TempDir(), "Book.m4b")
	if err := os.WriteFile(mediaPath, []byte("not read by fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	absolutePath, err := filepath.Abs(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXPECTED_MEDIA_PATH", absolutePath)
	binary := writeProbeFixture(t, `
test "$#" -eq 7
test "$1" = "-v"
test "$2" = "error"
test "$3" = "-show_entries"
test "$4" = "format=duration:format_tags:stream=codec_type,codec_name,width,height"
test "$5" = "-of"
test "$6" = "json"
test "$7" = "$EXPECTED_MEDIA_PATH"
printf '%s' '{"format":{"duration":"2"},"streams":[{"codec_type":"audio","codec_name":"aac"}]}'
`)

	got, err := NewFFProbe(binary).Probe(context.Background(), mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Duration != 2*time.Second {
		t.Fatalf("duration = %s", got.Duration)
	}
}

func TestFFProbeErrorsNeverRevealAbsoluteMediaPath(t *testing.T) {
	requirePOSIX(t)
	mediaPath := filepath.Join(t.TempDir(), "private", "Book.m4b")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := writeProbeFixture(t, `
echo "cannot inspect $7" >&2
exit 9
`)

	_, err := NewFFProbe(binary).Probe(context.Background(), mediaPath)
	if err == nil {
		t.Fatal("expected probe failure")
	}
	if strings.Contains(err.Error(), mediaPath) || strings.Contains(err.Error(), filepath.Dir(mediaPath)) {
		t.Fatalf("error leaked absolute path: %v", err)
	}
}

func TestFFProbeCapsStdoutAndStderr(t *testing.T) {
	requirePOSIX(t)
	mediaPath := filepath.Join(t.TempDir(), "Book.m4b")
	if err := os.WriteFile(mediaPath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body string
	}{
		{name: "stdout", body: `head -c 4194305 /dev/zero`},
		{name: "stderr", body: `head -c 1048577 /dev/zero >&2; exit 1`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binary := writeProbeFixture(t, test.body)
			_, err := NewFFProbe(binary).Probe(context.Background(), mediaPath)
			if !errors.Is(err, ErrProbeOutputTooLarge) {
				t.Fatalf("error = %v, want ErrProbeOutputTooLarge", err)
			}
		})
	}
}

func TestFFProbeTimesOut(t *testing.T) {
	requirePOSIX(t)
	mediaPath := filepath.Join(t.TempDir(), "Book.m4b")
	if err := os.WriteFile(mediaPath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := writeProbeFixture(t, `sleep 1`)
	probe := &FFProbe{
		binary:      binary,
		timeout:     25 * time.Millisecond,
		stdoutLimit: defaultStdoutLimit,
		stderrLimit: defaultStderrLimit,
	}

	started := time.Now()
	_, err := probe.Probe(context.Background(), mediaPath)
	if !errors.Is(err, ErrProbeTimeout) {
		t.Fatalf("error = %v, want ErrProbeTimeout", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout returned after %s, want a bounded child-process shutdown", elapsed)
	}
}

func TestFFProbeOpenFileFailsClosedWithoutDescriptorInheritance(t *testing.T) {
	mediaPath := filepath.Join(t.TempDir(), "Book.m4b")
	if err := os.WriteFile(mediaPath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	_, err = NewFFProbe("must-not-run").probeOpenFileForOS(
		context.Background(),
		file,
		"windows",
	)
	if !errors.Is(err, ErrProbeFailed) {
		t.Fatalf("error = %v, want fail-closed ErrProbeFailed", err)
	}
}

func requirePOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable script")
	}
}

func writeProbeFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe-fixture")
	content := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
