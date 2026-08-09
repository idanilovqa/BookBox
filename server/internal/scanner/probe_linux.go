//go:build linux

package scanner

import (
	"context"
	"fmt"
	"os"

	"bookbox.local/server/internal/library"
	"bookbox.local/server/internal/media"
)

func probeOpenedFile(
	ctx context.Context,
	probe media.Probe,
	_ *library.Resolver,
	_, _ string,
	handle *os.File,
	_ library.ResolvedPath,
) (media.Metadata, error) {
	if descriptorProbe, ok := probe.(media.OpenFileProbe); ok {
		return descriptorProbe.ProbeOpenFile(ctx, handle)
	}
	descriptorPath := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), handle.Fd())
	return probe.Probe(ctx, descriptorPath)
}
