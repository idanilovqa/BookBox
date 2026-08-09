//go:build !linux && !windows

package scanner

import (
	"context"
	"os"

	"bookbox.local/server/internal/library"
	"bookbox.local/server/internal/media"
)

func probeOpenedFile(
	context.Context,
	media.Probe,
	*library.Resolver,
	string,
	string,
	*os.File,
	library.ResolvedPath,
) (media.Metadata, error) {
	return media.Metadata{}, media.ErrProbeFailed
}
