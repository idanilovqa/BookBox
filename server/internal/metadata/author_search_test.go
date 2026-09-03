package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestSearchTitlePreservesMeaningfulNumbers(t *testing.T) {
	for raw, want := range map[string]string{
		"M01: tHe final Empire":              "tHe final Empire",
		"01 - The Final Empire (Unabridged)": "The Final Empire",
		"1984":                               "1984", "2001: A Space Odyssey": "2001: A Space Odyssey",
		"Fahrenheit 451": "Fahrenheit 451", "Catch-22": "Catch-22",
	} {
		if got := SearchTitle(raw); got != want {
			t.Errorf("%q -> %q want %q", raw, got, want)
		}
	}
}
func TestOpenLibraryAuthorSearchPagesAndCaches(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("author") != "Brandon Sanderson" || q.Get("limit") != "100" || q.Get("title") != "" {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		offset, _ := strconv.Atoi(q.Get("offset"))
		count := 100
		if offset == 100 {
			count = 1
		}
		docs := make([]map[string]any, 0, count)
		for i := 0; i < count; i++ {
			docs = append(docs, map[string]any{"key": "/works/" + strconv.Itoa(offset+i), "title": "Book " + strconv.Itoa(offset+i)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"docs": docs})
	}))
	defer server.Close()
	p, err := NewOpenLibrary(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := p.Search(context.Background(), Query{Author: "Brandon Sanderson"})
		if err != nil || len(got) != 101 {
			t.Fatalf("len=%d err=%v", len(got), err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls=%d want two pages shared by both searches", calls)
	}
}
