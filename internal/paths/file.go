package paths

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// OpenPrivateFile opens a regular data file, creating it if requested, and
// repairs its permissions to 0600 before use. Refuse symlinks and special files
// so opening a pre-existing path cannot redirect writes or block on a FIFO.
func OpenPrivateFile(path string, flag int) (*os.File, error) {
	f, err := os.OpenFile(path, flag|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", path)
	}
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
