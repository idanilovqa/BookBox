package catalog

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestOptionalSubjectsDoNotBlockCachedConfirmation(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"title": "The Selfish Gene", "authors": []string{"Richard Dawkins"}, "subjects": []string{"Genetics.\x1e 0", "Evolution"}})
	candidate, err := decodeConfirmedCandidate(raw, MetadataOpenLibrary, "/works/one")
	if err != nil || len(candidate.Subjects) != 1 || candidate.Subjects[0] != "Evolution" {
		t.Fatalf("valid identity rejected: %+v %v", candidate, err)
	}
	raw, _ = json.Marshal(map[string]any{"title": "Unsafe\x00Title", "authors": []string{"Author"}})
	if _, err = decodeConfirmedCandidate(raw, MetadataOpenLibrary, "/works/one"); !errors.Is(err, ErrInvalidData) {
		t.Fatal("identity validation weakened")
	}
}
