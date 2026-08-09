package workerlog

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const logFileName = "catalog-worker.log"

var (
	ErrInvalidPolicy      = errors.New("invalid log rotation policy")
	ErrEntryTooLarge      = errors.New("log entry exceeds file limit")
	ErrClosed             = errors.New("log writer is closed")
	ErrUnsafeTarget       = errors.New("unsafe log target")
	openLiveFileForWriter = openLiveFile
)

type rotatingWriter struct {
	mu        sync.Mutex
	directory string
	maxBytes  int64
	backups   int
	file      *os.File
	size      int64
	closed    bool
}

func Open(directory string, maxBytes int64, backups int) (io.WriteCloser, error) {
	if maxBytes <= 0 || backups < 1 {
		return nil, ErrInvalidPolicy
	}
	if err := validateDirectory(directory); err != nil {
		return nil, err
	}
	for index := 0; index <= backups; index++ {
		if err := validateManagedFile(logPath(directory, index)); err != nil {
			return nil, err
		}
	}
	if err := pruneStaleBackups(directory, backups); err != nil {
		return nil, err
	}

	file, size, err := openLiveFileForWriter(directory)
	if err != nil {
		return nil, err
	}
	return &rotatingWriter{
		directory: directory,
		maxBytes:  maxBytes,
		backups:   backups,
		file:      file,
		size:      size,
	}, nil
}

func (writer *rotatingWriter) Write(entry []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	if writer.closed {
		return 0, ErrClosed
	}
	if writer.file == nil {
		file, size, err := openLiveFileForWriter(writer.directory)
		if err != nil {
			return 0, err
		}
		writer.file = file
		writer.size = size
	}
	if int64(len(entry)) > writer.maxBytes {
		return 0, ErrEntryTooLarge
	}
	if len(entry) == 0 {
		return 0, nil
	}
	if writer.size > writer.maxBytes-int64(len(entry)) {
		if err := writer.rotate(); err != nil {
			return 0, err
		}
	}
	written, err := writer.file.Write(entry)
	writer.size += int64(written)
	return written, err
}

func (writer *rotatingWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return nil
	}
	writer.closed = true
	if writer.file == nil {
		return nil
	}
	err := writer.file.Close()
	writer.file = nil
	return err
}

func (writer *rotatingWriter) rotate() error {
	if err := validateDirectory(writer.directory); err != nil {
		return err
	}
	for index := 0; index <= writer.backups; index++ {
		if err := validateManagedFile(logPath(writer.directory, index)); err != nil {
			return err
		}
	}
	closeErr := writer.file.Close()
	writer.file = nil
	if closeErr != nil {
		file, size, reopenErr := openLiveFileForWriter(writer.directory)
		if reopenErr == nil {
			writer.file = file
			writer.size = size
		}
		return errors.Join(closeErr, reopenErr)
	}

	rotationErr := writer.rotateFiles()
	file, size, reopenErr := openLiveFileForWriter(writer.directory)
	if reopenErr == nil {
		writer.file = file
		writer.size = size
	}
	if rotationErr != nil {
		return errors.Join(rotationErr, reopenErr)
	}
	return reopenErr
}

func (writer *rotatingWriter) rotateFiles() error {
	oldest := logPath(writer.directory, writer.backups)
	if err := removeIfExists(oldest); err != nil {
		return err
	}
	for index := writer.backups - 1; index >= 1; index-- {
		if err := renameIfExists(
			logPath(writer.directory, index),
			logPath(writer.directory, index+1),
		); err != nil {
			return err
		}
	}
	return renameIfExists(logPath(writer.directory, 0), logPath(writer.directory, 1))
}

func openLiveFile(directory string) (*os.File, int64, error) {
	path := logPath(directory, 0)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, 0, ErrUnsafeTarget
	}
	return file, info.Size(), nil
}

func logPath(directory string, index int) string {
	path := filepath.Join(directory, logFileName)
	if index == 0 {
		return path
	}
	return path + "." + strconv.Itoa(index)
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func renameIfExists(source, destination string) error {
	if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return os.Rename(source, destination)
}

func pruneStaleBackups(directory string, backups int) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var stale []string
	for _, entry := range entries {
		if !isStaleBackupName(entry.Name(), backups) {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if err := validateManagedFile(path); err != nil {
			return err
		}
		stale = append(stale, path)
	}
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func isStaleBackupName(name string, backups int) bool {
	prefix := logFileName + "."
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return false
	}
	suffix := name[len(prefix):]
	if suffix[0] == '0' {
		return false
	}
	for _, character := range suffix {
		if character < '0' || character > '9' {
			return false
		}
	}
	limit := strconv.Itoa(backups)
	return len(suffix) > len(limit) || len(suffix) == len(limit) && suffix > limit
}
