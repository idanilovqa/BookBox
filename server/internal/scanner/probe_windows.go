//go:build windows

package scanner

import (
	"context"
	"os"

	"golang.org/x/sys/windows"

	"bookbox.local/server/internal/library"
	"bookbox.local/server/internal/media"
)

type windowsFileIdentity struct {
	VolumeSerial  uint32
	FileIndexHigh uint32
	FileIndexLow  uint32
	Size          int64
	Modified      int64
}

func probeOpenedFile(
	ctx context.Context,
	probe media.Probe,
	resolver *library.Resolver,
	rootID, relative string,
	handle *os.File,
	resolved library.ResolvedPath,
) (media.Metadata, error) {
	before, err := windowsIdentity(handle)
	if err != nil {
		return media.Metadata{}, media.ErrProbeFailed
	}
	metadata, probeErr := probe.Probe(ctx, resolved.Absolute)
	reopened, afterResolved, err := resolver.OpenRegular(rootID, relative)
	if err != nil {
		return media.Metadata{}, media.ErrProbeFailed
	}
	defer reopened.Close()
	if afterResolved.Absolute != resolved.Absolute {
		return media.Metadata{}, media.ErrProbeFailed
	}
	after, err := windowsIdentity(reopened)
	if err != nil || before != after {
		return media.Metadata{}, media.ErrProbeFailed
	}
	return metadata, probeErr
}

func windowsIdentity(file *os.File) (windowsFileIdentity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return windowsFileIdentity{}, err
	}
	return windowsFileIdentity{
		VolumeSerial:  info.VolumeSerialNumber,
		FileIndexHigh: info.FileIndexHigh,
		FileIndexLow:  info.FileIndexLow,
		Size:          int64(uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)),
		Modified:      info.LastWriteTime.Nanoseconds(),
	}, nil
}
