package covers

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"

	_ "golang.org/x/image/webp"
)

const (
	MaxInputBytes = 20 << 20
	MaxPixels     = 100_000_000
)

var (
	ErrImageTooLarge    = errors.New("cover image exceeds limits")
	ErrUnsupportedImage = errors.New("unsupported cover image")
	ErrInvalidImage     = errors.New("invalid cover image")
)

type ImageInfo struct {
	Width  int
	Height int
	Format string
}

func Inspect(reader io.Reader, declaredSize int64) (ImageInfo, []byte, error) {
	if declaredSize > MaxInputBytes {
		return ImageInfo{}, nil, ErrImageTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxInputBytes+1))
	if err != nil {
		return ImageInfo{}, nil, fmt.Errorf("read cover image: %w", err)
	}
	if len(data) > MaxInputBytes {
		return ImageInfo{}, nil, ErrImageTooLarge
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return ImageInfo{}, nil, fmt.Errorf("%w: %v", ErrInvalidImage, err)
	}
	if format != "jpeg" && format != "png" && format != "webp" {
		return ImageInfo{}, nil, ErrUnsupportedImage
	}
	if config.Width <= 0 || config.Height <= 0 ||
		int64(config.Width) > MaxPixels/int64(config.Height) {
		return ImageInfo{}, nil, ErrImageTooLarge
	}
	if _, decodedFormat, err := image.Decode(bytes.NewReader(data)); err != nil {
		return ImageInfo{}, nil, fmt.Errorf("%w: %v", ErrInvalidImage, err)
	} else if decodedFormat != format {
		return ImageInfo{}, nil, ErrInvalidImage
	}
	return ImageInfo{Width: config.Width, Height: config.Height, Format: format}, data, nil
}

func ShouldReplace(current, candidate ImageInfo) bool {
	if candidate.Width <= 0 || candidate.Height <= 0 {
		return false
	}
	if current.Width <= 0 || current.Height <= 0 {
		return true
	}
	if candidate.Width < current.Width || candidate.Height < current.Height {
		return false
	}
	currentArea := int64(current.Width) * int64(current.Height)
	candidateArea := int64(candidate.Width) * int64(candidate.Height)
	return candidateArea >= currentArea*2
}
