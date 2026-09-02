package scanner

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

func TestInspectImageHeaderRecordsValidPNGDimensions(t *testing.T) {
	data := pngHeader(600, 900)
	width, height, format, ok := inspectImageHeader(bytes.NewReader(data), int64(len(data)))
	if !ok || width != 600 || height != 900 || format != "png" {
		t.Fatalf("image = %dx%d %q ok=%v", width, height, format, ok)
	}
}

func TestInspectImageHeaderRejectsInvalidAndOversizedDimensions(t *testing.T) {
	if _, _, _, ok := inspectImageHeader(bytes.NewReader([]byte("not an image")), 12); ok {
		t.Fatal("invalid image header accepted")
	}
	oversized := pngHeader(10_001, 10_000)
	if _, _, _, ok := inspectImageHeader(bytes.NewReader(oversized), int64(len(oversized))); ok {
		t.Fatal("oversized image dimensions accepted")
	}
}

func pngHeader(width, height uint32) []byte {
	data := append([]byte(nil), []byte("\x89PNG\r\n\x1a\n")...)
	chunk := make([]byte, 13)
	binary.BigEndian.PutUint32(chunk[0:4], width)
	binary.BigEndian.PutUint32(chunk[4:8], height)
	chunk[8] = 8
	chunk[9] = 2
	data = binary.BigEndian.AppendUint32(data, uint32(len(chunk)))
	data = append(data, "IHDR"...)
	data = append(data, chunk...)
	checksum := crc32.ChecksumIEEE(append([]byte("IHDR"), chunk...))
	return binary.BigEndian.AppendUint32(data, checksum)
}
