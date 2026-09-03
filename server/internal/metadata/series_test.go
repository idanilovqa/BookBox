package metadata

import (
	"bookbox.local/server/internal/catalog"
	"testing"
)

func TestInferSeriesIdentity(t *testing.T) {
	for _, tc := range []struct {
		name             string
		book             catalog.InferredBook
		series, position string
	}{
		{"provider", catalog.InferredBook{Title: "The Great Hunt (The Wheel of Time Book 2)"}, "The Wheel of Time", "2"},
		{"comma", catalog.InferredBook{Title: "The Dragon Reborn (The Wheel of Time, Book 3)"}, "The Wheel of Time", "3"},
		{"folder", catalog.InferredBook{Title: "The Eye of the World", Authors: []string{"Robert Jordan"}, SourceFolder: "Robert Jordan/The Wheel of Time/01 The Eye of the World"}, "The Wheel of Time", "1"},
		{"shared folder", catalog.InferredBook{Title: "(#11) Reaper Man", Authors: []string{"Terry Pratchett"}, SourceFolder: "Terry Pratchett/Discworld"}, "Discworld", "11"},
		{"decimal", catalog.InferredBook{Title: "Side Story", Authors: []string{"Author Name"}, SourceFolder: "Author Name/Example Series/02.5 Side Story"}, "Example Series", "2.5"},
		{"standalone", catalog.InferredBook{Title: "01 The Great Train Robbery", Authors: []string{"Michael Crichton"}, SourceFolder: "Michael Crichton/1975 - The Great Train Robbery"}, "", ""},
		{"numeric title", catalog.InferredBook{Title: "12 Years a Slave", Authors: []string{"Solomon Northup"}, SourceFolder: "Solomon Northup/12 Years a Slave"}, "", ""},
		{"existing", catalog.InferredBook{Title: "The Great Hunt (Another Series Book 2)", Series: "User Series", SeriesPosition: "4"}, "User Series", "4"},
		{"partial", catalog.InferredBook{Title: "The Great Hunt (Volume 1)"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := InferSeriesIdentity(tc.book)
			if s != tc.series || p != tc.position {
				t.Fatalf("got %q %q, want %q %q", s, p, tc.series, tc.position)
			}
		})
	}
}

func TestCandidateRetainsSeriesFromNumberedLocalTitle(t *testing.T) {
	c := candidateWithSeries(Candidate{Title: "Reaper Man"}, catalog.InferredBook{Title: "(#11) Reaper Man", Authors: []string{"Terry Pratchett"}, SourceFolder: "Terry Pratchett/Discworld"})
	if c.Series != "Discworld" || c.SeriesPosition != "11" {
		t.Fatalf("lost local series: %+v", c)
	}
}

func TestSeriesEvidencePrecedence(t *testing.T) {
	local := catalog.InferredBook{Title: "01 The Great Train Robbery", SourceFolder: "Michael Crichton/01 The Great Train Robbery"}
	if s, p := InferSeriesIdentity(local); s != "" || p != "" {
		t.Fatalf("author inferred as series: %s %s", s, p)
	}
	c := candidateWithSeries(Candidate{Title: "The Great Train Robbery", Authors: []string{"Michael Crichton"}}, local)
	if c.Series != "" {
		t.Fatalf("provider author inferred as series: %+v", c)
	}
	c = candidateWithSeries(Candidate{Title: "The Great Hunt (The Wheel of Time Book 2)"}, catalog.InferredBook{Title: "02 The Great Hunt", Authors: []string{"Robert Jordan"}, SourceFolder: "Robert Jordan/Fantasy/02 The Great Hunt"})
	if c.Series != "The Wheel of Time" || c.SeriesPosition != "2" {
		t.Fatalf("provider label lost: %+v", c)
	}
}

func TestConfirmedSeriesUsesActualBookFolder(t *testing.T) {
	book := catalog.InferredBook{Title: "Reaper Man", Authors: []string{"Terry Pratchett"}, SourceFolder: "Terry Pratchett/Complete Discworld/Terry Pratchett"}
	files := []catalog.SourceFile{{FileKind: catalog.FileKindAudio, RelativePath: "Terry Pratchett/Complete Discworld/Terry Pratchett/Discworld/(#11) Reaper Man/(#11) Reaper Man.m4b"}}
	s, p := InferSeriesFromFiles(book, files)
	if s != "Discworld" || p != "11" {
		t.Fatalf("got %s %s", s, p)
	}
	files[0].RelativePath = "Terry Pratchett/Reaper Man/CD01 - Reaper Man/track01.mp3"
	if s, p := InferSeriesFromFiles(book, files); s != "" || p != "" {
		t.Fatalf("disc mistaken for book number: %s %s", s, p)
	}
}

func TestSeriesRejectsCollectionAndNarratorFolders(t *testing.T) {
	for _, folder := range []string{"Robin Hobb/01 Novels/01 Assassin's Apprentice", "James Corey/narrated by Jefferson Mays/01 Assassin's Apprentice"} {
		s, p := InferSeriesIdentity(catalog.InferredBook{Title: "Assassin's Apprentice", Authors: []string{"Robin Hobb"}, SourceFolder: folder})
		if s != "" || p != "" {
			t.Fatalf("collection inferred: %s %s", s, p)
		}
	}
	s, p := InferSeriesIdentity(catalog.InferredBook{Title: "[The Expanse 0.1] Drive (novella)", Authors: []string{"James S. A. Corey"}, SourceFolder: "James Corey/narrated by Jefferson Mays/2.7 - Drive"})
	if s != "The Expanse" || p != "0.1" {
		t.Fatalf("explicit label lost: %s %s", s, p)
	}
}

func TestSeriesFolderWithAuthorDecoration(t *testing.T) {
	for _, tc := range []struct{ title, folder, position string }{
		{"M01: The Final Empire", "Brandon Sanderson/Mistborn Series - Brandon Sanderson", "1"},
		{"M03: The Hero of Ages", "Brandon Sanderson/Mistborn Series - Brandon Sanderson", "3"},
		{"The Final Empire", "Brandon Sanderson/Mistborn Series - Brandon Sanderson/M01_ The Final Empire", "1"},
		{"The Hero of Ages", "Brandon Sanderson/Mistborn Series - Brandon Sanderson/M03__The Hero of Ages", "3"},
		{"The Final Empire", "Brandon Sanderson/Brandon Sanderson - Mistborn Series/M01_ The Final Empire", "1"},
	} {
		t.Run(tc.folder+tc.title, func(t *testing.T) {
			s, p := InferSeriesIdentity(catalog.InferredBook{Title: tc.title, Authors: []string{"Brandon Sanderson"}, SourceFolder: tc.folder})
			if s != "Mistborn" || p != tc.position {
				t.Fatalf("got %q %q", s, p)
			}
		})
	}
	for _, folder := range []string{"Brandon Sanderson/Brandon Sanderson/M01_ The Final Empire", "Brandon Sanderson/Brandon Sanderson Collection/M01_ The Final Empire"} {
		if s, p := InferSeriesIdentity(catalog.InferredBook{Title: "The Final Empire", Authors: []string{"Brandon Sanderson"}, SourceFolder: folder}); s != "" || p != "" {
			t.Fatalf("author folder became series: %s %s", s, p)
		}
	}
}
