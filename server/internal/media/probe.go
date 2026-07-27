package media

import (
	"context"
	"errors"
	"os"
	"time"
)

var (
	ErrUnsupportedMedia    = errors.New("media has no audio stream")
	ErrInvalidProbeOutput  = errors.New("ffprobe returned invalid output")
	ErrProbeOutputTooLarge = errors.New("ffprobe output exceeds limit")
	ErrProbeTimeout        = errors.New("ffprobe timed out")
	ErrProbeFailed         = errors.New("ffprobe failed")
)

type Metadata struct {
	Duration   time.Duration
	Title      string
	Album      string
	Author     string
	Artist     string
	Narrator   string
	Genre      string
	Date       string
	AudioCodec string
	Width      int
	Height     int
}

type Probe interface {
	Probe(context.Context, string) (Metadata, error)
}

// OpenFileProbe lets a scanner pass a descriptor that was opened beneath a
// configured library root without following symlinks.
type OpenFileProbe interface {
	ProbeOpenFile(context.Context, *os.File) (Metadata, error)
}
