package covers

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestInspectRecognizedImages(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		data   []byte
		format string
	}{
		{name: "jpeg", data: testJPEG(t, 32, 48), format: "jpeg"},
		{name: "png", data: testPNG(t, 32, 48), format: "png"},
		{name: "webp", data: testWebP, format: "webp"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			info, data, err := Inspect(bytes.NewReader(test.data), int64(len(test.data)))
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}
			if info.Format != test.format || info.Width <= 0 || info.Height <= 0 {
				t.Fatalf("Inspect() info = %#v", info)
			}
			if !bytes.Equal(data, test.data) {
				t.Fatal("Inspect() changed source bytes")
			}
		})
	}
}

func TestInspectRejectsUnsupportedOversizedAndBombImages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data []byte
	}{
		{name: "unsupported", data: []byte("GIF89a")},
		{name: "oversized", data: bytes.Repeat([]byte{0x41}, MaxInputBytes+1)},
		{name: "zero width", data: pngHeader(0, 100)},
		{name: "over 100 megapixels", data: pngHeader(10_001, 10_000)},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := Inspect(bytes.NewReader(test.data), int64(len(test.data))); err == nil {
				t.Fatal("Inspect() error = nil")
			}
		})
	}
}

func TestShouldReplaceRequiresMaterialImprovementWithoutShrinking(t *testing.T) {
	t.Parallel()

	current := ImageInfo{Width: 600, Height: 900, Format: "jpeg"}
	if !ShouldReplace(current, ImageInfo{Width: 1200, Height: 1800, Format: "jpeg"}) {
		t.Fatal("2x dimensions should replace")
	}
	if ShouldReplace(current, ImageInfo{Width: 700, Height: 1000, Format: "jpeg"}) {
		t.Fatal("less than 2x area should not replace")
	}
	if ShouldReplace(current, ImageInfo{Width: 500, Height: 2000, Format: "jpeg"}) {
		t.Fatal("a smaller dimension should not replace")
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

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := jpeg.Encode(&output, testImage(width, height), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := png.Encode(&output, testImage(width, height)); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testImage(width, height int) image.Image {
	result := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			result.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x7f, A: 0xff})
		}
	}
	return result
}

var testWebP = mustDecodeBase64(
	"UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA==",
)

func mustDecodeBase64(value string) []byte {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return data
}
