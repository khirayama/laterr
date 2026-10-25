// Package paths locates laterr's files on disk.
package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

// HomeEnv overrides the data directory. It exists for tests and development:
// when it is set, laterr never installs or removes the background service, so
// the scheduler has to be started by hand with `laterr daemon`.
const HomeEnv = "LATERR_HOME"

// Paths are the files laterr keeps, all of them inside the data directory.
type Paths struct {
	Dir          string // data directory, 0700
	DB           string
	Logs         string // per-job logs
	Socket       string
	Lock         string // held by the running scheduler
	SchedulerLog string // the scheduler's own output (launchd only)
	MenuBarOff   string // exists if the menu bar icon was turned off (macOS)
}

// Resolve returns the paths without creating anything.
func Resolve() (Paths, error) {
	dir := os.Getenv(HomeEnv)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, err
		}
		if runtime.GOOS == "darwin" {
			dir = filepath.Join(home, "Library", "Application Support", "laterr")
		} else if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
			dir = filepath.Join(xdg, "laterr")
		} else {
			dir = filepath.Join(home, ".local", "share", "laterr")
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Paths{}, err
	}
	// Unix socket addresses are limited to about 100 bytes.
	if len(filepath.Join(dir, "laterr.sock")) > 100 {
		return Paths{}, errors.New("data directory path is too long for a Unix socket: " + dir)
	}
	return Paths{
		Dir:          dir,
		DB:           filepath.Join(dir, "laterr.db"),
		Logs:         filepath.Join(dir, "logs"),
		Socket:       filepath.Join(dir, "laterr.sock"),
		Lock:         filepath.Join(dir, "scheduler.lock"),
		SchedulerLog: filepath.Join(dir, "scheduler.log"),
		MenuBarOff:   filepath.Join(dir, "menubar.off"),
	}, nil
}

// ServiceManaged reports whether laterr may install and remove the OS service.
func ServiceManaged() bool {
	return os.Getenv(HomeEnv) == ""
}

// EnsureDirs creates the data and log directories and repairs their permissions.
// The final components must be real directories, not symbolic links.
func (p Paths) EnsureDirs() error {
	for _, d := range []string{p.Dir, p.Logs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(d, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		err = f.Chmod(0o700)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// LogFile returns the log path for a job.
func (p Paths) LogFile(id int64) string {
	return filepath.Join(p.Logs, strconv.FormatInt(id, 10)+".log")
}
