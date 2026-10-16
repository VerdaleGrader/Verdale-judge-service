package pipeline

import (
	"errors"
	"os"
	"syscall"
)

var errNotRegular = errors.New("not a regular file")

// openSandboxFile opens a file that a sandboxed program could have touched.
//
// The program owns its box directory, so it can delete the output file and
// put a link to a host file (or a named pipe, which blocks a reader forever)
// in its place. isolate removes such special files after a run, but Judge
// does not rely on that alone: a link is never followed and anything but a
// regular file is refused. Both count as "no such file" to the callers.
func openSandboxFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			return nil, errNotRegular
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, errNotRegular
	}
	return file, nil
}

// isAbsent is true for a file that is missing or was replaced by something
// that is not a regular file.
func isAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, errNotRegular)
}

// regularFileSize is the size of a regular file, or false for a missing one
// or anything else.
func regularFileSize(path string) (int64, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return info.Size(), true
}
