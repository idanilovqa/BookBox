package scanner

import (
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"

	_ "golang.org/x/image/webp"
)

const (
	maxImageHeaderBytes int64 = 512 << 10
	maxImagePixels      int64 = 100_000_000
)

func inspectImageHeader(reader io.Reader, declaredSize int64) (int, int, string, bool) {
	if reader == nil || declaredSize <= 0 {
		return 0, 0, "", false
	}
	config, format, err := image.DecodeConfig(io.LimitReader(reader, maxImageHeaderBytes))
	if err != nil || (format != "jpeg" && format != "png" && format != "webp") ||
		config.Width <= 0 || config.Height <= 0 ||
		int64(config.Width) > maxImagePixels/int64(config.Height) {
		return 0, 0, "", false
	}
	return config.Width, config.Height, format, true
}
