package companion

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestParseExtractsNormalizedCompanionEvidence(t *testing.T) {
	tests := []struct {
		name string
		kind string
		body string
		want Evidence
	}{
		{
			name: "cue",
			kind: "cue",
			body: "\ufeff  PERFORMER  \" Frank   Herbert \"\r\n" +
				"TITLE \" Dune \"\r\n" +
				"FILE \"Disc 1\\01 - Dune.mp3\" MP3\r\n" +
				"  TRACK 01 AUDIO\r\n" +
				"    TITLE \"Chapter One\"\r\n" +
				"FILE \"Disc 1/01 - Dune.mp3\" MP3\r\n" +
				"FILE \"Disc 2/02 - Dune.flac\" WAVE\r\n",
			want: Evidence{
				Title:           "Dune",
				Creators:        []string{"Frank Herbert"},
				ReferencedFiles: []string{"Disc 1/01 - Dune.mp3", "Disc 2/02 - Dune.flac"},
			},
		},
		{
			name: "nfo",
			kind: "nfo",
			body: " Title :  The   Left Hand of Darkness \r\n" +
				"Author: Ursula K. Le Guin\r\n" +
				"Creator =  Ursula K. Le Guin \r\n" +
				"Narrator: ignored for grouping\r\n" +
				"File: Disc 1\\01.mp3\r\n" +
				"Filename = Disc 2/02.flac\r\n",
			want: Evidence{
				Title:           "The Left Hand of Darkness",
				Creators:        []string{"Ursula K. Le Guin"},
				ReferencedFiles: []string{"Disc 1/01.mp3", "Disc 2/02.flac"},
			},
		},
		{
			name: "opf",
			kind: "opf",
			body: `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>  La   ciudad de las bestias </dc:title>
    <dc:creator> Isabel Allende </dc:creator>
    <dc:creator>Isabel Allende</dc:creator>
  </metadata>
  <manifest>
    <item id="two" href="Audio%20Book/02.mp3" media-type="audio/mpeg"/>
    <item id="cover" href="images/cover.jpg" media-type="image/jpeg"/>
    <item id="one" href="Audio%20Book\01.mp3" media-type="audio/mpeg"/>
  </manifest>
  <spine><itemref idref="one"/><itemref idref="two"/></spine>
</package>`,
			want: Evidence{
				Title:           "La ciudad de las bestias",
				Creators:        []string{"Isabel Allende"},
				ReferencedFiles: []string{"Audio Book/01.mp3", "Audio Book/02.mp3", "images/cover.jpg"},
				PlaylistOrder:   []string{"Audio Book/01.mp3", "Audio Book/02.mp3"},
			},
		},
		{
			name: "m3u",
			kind: "m3u",
			body: "\ufeff#EXTM3U\r\n#PLAYLIST:  Dune  \r\n" +
				"#EXTINF:10,Chapter 2\r\nDisc 1\\02.mp3\r\n" +
				"# a comment\r\nDisc 1/01.mp3\r\nDisc 1/02.mp3\r\n",
			want: Evidence{
				Title:           "Dune",
				ReferencedFiles: []string{"Disc 1/01.mp3", "Disc 1/02.mp3"},
				PlaylistOrder:   []string{"Disc 1/02.mp3", "Disc 1/01.mp3"},
			},
		},
		{
			name: "m3u8 preserves unicode",
			kind: "m3u8",
			body: "#EXTM3U\n#PLAYLIST: Земноморье\nАудио/01.mp3\nАудио/02.mp3\n",
			want: Evidence{
				Title:           "Земноморье",
				ReferencedFiles: []string{"Аудио/01.mp3", "Аудио/02.mp3"},
				PlaylistOrder:   []string{"Аудио/01.mp3", "Аудио/02.mp3"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.kind, strings.NewReader(test.body), 2<<20)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Parse() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseReturnsDeterministicEvidence(t *testing.T) {
	body := "Author: Octavia E. Butler\n" +
		"File: z/03.mp3\n" +
		"Title:  Parable   of the Sower\n" +
		"Creator: Octavia E. Butler\n" +
		"Filename: a\\01.mp3\n" +
		"File: z/03.mp3\n"
	want := Evidence{
		Title:           "Parable of the Sower",
		Creators:        []string{"Octavia E. Butler"},
		ReferencedFiles: []string{"a/01.mp3", "z/03.mp3"},
	}

	for attempt := 0; attempt < 20; attempt++ {
		got, err := Parse("NFO", strings.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("attempt %d: Parse() = %#v, want %#v", attempt, got, want)
		}
	}
}

func TestParseRejectsMalformedOrInvalidInputWithSanitizedErrors(t *testing.T) {
	const secret = `C:\private\owner\Secret Book`
	tests := []struct {
		name string
		kind string
		body string
		want error
	}{
		{name: "malformed cue", kind: "cue", body: `FILE "` + secret, want: ErrMalformedInput},
		{name: "malformed opf", kind: "opf", body: `<package><metadata><title>` + secret, want: ErrMalformedInput},
		{name: "invalid utf8 nfo", kind: "nfo", body: string([]byte{'T', 'i', 't', 'l', 'e', ':', 0xff}), want: ErrInvalidEncoding},
		{name: "nul in playlist", kind: "m3u", body: "safe.mp3\x00" + secret, want: ErrInvalidEncoding},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse(test.kind, strings.NewReader(test.body), 2<<20)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "Secret Book") {
				t.Fatalf("error leaked companion content: %v", err)
			}
		})
	}
}

func TestParseCapsEverySupportedFormatBeforeParsing(t *testing.T) {
	const limit = int64(64)
	for _, kind := range []string{"cue", "nfo", "opf", "m3u", "m3u8"} {
		t.Run(kind, func(t *testing.T) {
			reader := &countingReader{remaining: limit + 128}
			_, err := Parse(kind, reader, limit)
			if !errors.Is(err, ErrInputTooLarge) {
				t.Fatalf("error = %v, want ErrInputTooLarge", err)
			}
			if reader.read > limit+1 {
				t.Fatalf("read %d bytes for limit %d; parser must stop after limit+1", reader.read, limit)
			}
		})
	}
}

func TestParseRejectsUnsupportedKindWithoutReading(t *testing.T) {
	_, err := Parse("ebook", panicReader{}, 2<<20)
	if !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("error = %v, want ErrUnsupportedKind", err)
	}
}

func TestParseRejectsInvalidLimitWithoutReading(t *testing.T) {
	for _, limit := range []int64{-1, 0} {
		_, err := Parse("cue", panicReader{}, limit)
		if !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("limit %d: error = %v, want ErrInvalidLimit", limit, err)
		}
	}
}

type countingReader struct {
	remaining int64
	read      int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return int(n), nil
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) {
	panic("unsupported kind or invalid limit must be rejected before reading")
}
