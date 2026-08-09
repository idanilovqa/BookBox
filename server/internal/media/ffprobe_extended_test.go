package media

import "testing"

func TestParseFFProbeExtractsExtendedAudiobookIdentity(t *testing.T) {
	metadata, err := parseFFProbe([]byte(`{
		"format": {
			"duration": "123.5",
			"tags": {
				"title": "The Chapter",
				"album": "The Book",
				"album_artist": "The Author",
				"series": "The Series",
				"track": "3/12",
				"disc": "2/3",
				"ISBN": "978-0-00-000000-1",
				"ASIN": "B000000001"
			}
		},
		"streams": [{"codec_type":"audio","codec_name":"aac"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Series != "The Series" || metadata.Track != "3/12" ||
		metadata.Disc != "2/3" || metadata.AlbumArtist != "The Author" {
		t.Fatalf("extended metadata = %#v", metadata)
	}
	wantIdentifiers := map[string]string{
		"isbn": "978-0-00-000000-1",
		"asin": "B000000001",
	}
	if len(metadata.Identifiers) != len(wantIdentifiers) {
		t.Fatalf("identifiers = %#v", metadata.Identifiers)
	}
	for key, want := range wantIdentifiers {
		if metadata.Identifiers[key] != want {
			t.Errorf("identifier %q = %q, want %q", key, metadata.Identifiers[key], want)
		}
	}
}
