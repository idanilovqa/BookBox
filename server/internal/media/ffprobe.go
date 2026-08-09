package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultProbeTimeout = 30 * time.Second
	defaultStdoutLimit  = 4 << 20
	defaultStderrLimit  = 1 << 20
)

type FFProbe struct {
	binary      string
	timeout     time.Duration
	stdoutLimit int
	stderrLimit int
}

func NewFFProbe(binary string) *FFProbe {
	if strings.TrimSpace(binary) == "" {
		binary = "ffprobe"
	}
	return &FFProbe{
		binary:      binary,
		timeout:     defaultProbeTimeout,
		stdoutLimit: defaultStdoutLimit,
		stderrLimit: defaultStderrLimit,
	}
}

func (p *FFProbe) Probe(ctx context.Context, path string) (Metadata, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Metadata{}, ErrProbeFailed
	}
	return p.run(ctx, absolute, nil)
}

func (p *FFProbe) ProbeOpenFile(ctx context.Context, file *os.File) (Metadata, error) {
	return p.probeOpenFileForOS(ctx, file, runtime.GOOS)
}

func (p *FFProbe) probeOpenFileForOS(
	ctx context.Context,
	file *os.File,
	goos string,
) (Metadata, error) {
	if file == nil {
		return Metadata{}, ErrProbeFailed
	}
	switch goos {
	case "linux":
		return p.run(ctx, "/proc/self/fd/3", []*os.File{file})
	case "windows":
		return p.runWithInput(ctx, "pipe:0", nil, file, file.Name())
	default:
		return Metadata{}, ErrProbeFailed
	}
}

func (p *FFProbe) run(
	ctx context.Context,
	input string,
	extraFiles []*os.File,
) (Metadata, error) {
	return p.runWithInput(ctx, input, extraFiles, nil, "")
}

func (p *FFProbe) runWithInput(
	ctx context.Context,
	input string,
	extraFiles []*os.File,
	stdin *os.File,
	sourceName string,
) (Metadata, error) {
	timeout := p.timeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(
		probeCtx,
		p.binary,
		"-v", "error",
		"-show_entries",
		"format=duration:format_tags:stream=codec_type,codec_name,width,height",
		"-of", "json",
		input,
	)
	command.ExtraFiles = extraFiles
	command.Stdin = stdin
	if sourceName != "" {
		command.Env = append(os.Environ(), "BOOKBOX_FFPROBE_INPUT_PATH="+sourceName)
	}
	command.WaitDelay = 100 * time.Millisecond
	stdout := newBoundedBuffer(p.stdoutLimit, defaultStdoutLimit)
	stderr := newBoundedBuffer(p.stderrLimit, defaultStderrLimit)
	command.Stdout = stdout
	command.Stderr = stderr

	runErr := command.Run()
	if stdout.exceeded || stderr.exceeded {
		return Metadata{}, ErrProbeOutputTooLarge
	}
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		if ctx.Err() != nil {
			return Metadata{}, ctx.Err()
		}
		return Metadata{}, ErrProbeTimeout
	}
	if ctx.Err() != nil {
		return Metadata{}, ctx.Err()
	}
	if runErr != nil {
		return Metadata{}, ErrProbeFailed
	}
	return parseFFProbe(stdout.Bytes())
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func newBoundedBuffer(configured, fallback int) *boundedBuffer {
	if configured <= 0 {
		configured = fallback
	}
	return &boundedBuffer{limit: configured}
}

func (b *boundedBuffer) Write(chunk []byte) (int, error) {
	originalLength := len(chunk)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return originalLength, nil
	}
	if len(chunk) > remaining {
		b.exceeded = true
		chunk = chunk[:remaining]
	}
	_, _ = b.buffer.Write(chunk)
	return originalLength, nil
}

func (b *boundedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

type ffprobeDocument struct {
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

func parseFFProbe(raw []byte) (Metadata, error) {
	var document ffprobeDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return Metadata{}, ErrInvalidProbeOutput
	}

	metadata := Metadata{}
	hasAudio := false
	for _, stream := range document.Streams {
		switch strings.ToLower(strings.TrimSpace(stream.CodecType)) {
		case "audio":
			if !hasAudio {
				metadata.AudioCodec = strings.TrimSpace(stream.CodecName)
			}
			hasAudio = true
		case "video":
			if stream.Width > metadata.Width || stream.Height > metadata.Height {
				metadata.Width = stream.Width
				metadata.Height = stream.Height
			}
		}
	}
	if !hasAudio {
		return Metadata{}, ErrUnsupportedMedia
	}

	if duration := strings.TrimSpace(document.Format.Duration); duration != "" {
		seconds, err := strconv.ParseFloat(duration, 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
			return Metadata{}, ErrInvalidProbeOutput
		}
		metadata.Duration = time.Duration(seconds * float64(time.Second))
	}
	tags := lowerCaseTags(document.Format.Tags)
	metadata.Title = tags["title"]
	metadata.Album = tags["album"]
	metadata.AlbumArtist = tags["album_artist"]
	metadata.Artist = tags["artist"]
	metadata.Author = firstNonEmpty(tags["author"], tags["album_artist"], tags["artist"])
	metadata.Narrator = firstNonEmpty(tags["narrator"], tags["performer"])
	metadata.Genre = tags["genre"]
	metadata.Date = firstNonEmpty(tags["date"], tags["year"])
	metadata.Series = firstNonEmpty(tags["series"], tags["show"])
	metadata.Track = firstNonEmpty(tags["track"], tags["tracknumber"])
	metadata.Disc = firstNonEmpty(tags["disc"], tags["discnumber"])
	metadata.Identifiers = map[string]string{}
	for _, key := range []string{"isbn", "asin", "isrc"} {
		if value := tags[key]; value != "" {
			metadata.Identifiers[key] = value
		}
	}
	return metadata, nil
}

func lowerCaseTags(tags map[string]string) map[string]string {
	result := make(map[string]string, len(tags))
	for key, value := range tags {
		result[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
