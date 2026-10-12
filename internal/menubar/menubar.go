// Package menubar shows the jobs that are waiting or running in the macOS
// menu bar. It is a convenience on top of the CLI, and optional: on by
// default on macOS, absent everywhere else.
//
// The icon belongs to a process of its own (`laterr __menubar`), which the
// scheduler starts while there is something to show. A fault in the user
// interface therefore cannot take the scheduler, or the jobs it runs, down
// with it.
package menubar

import (
	"errors"
	"os"
	"runtime"

	"github.com/khirayama/laterr/internal/paths"
)

// Supported reports whether this system has a menu bar to show the icon in.
func Supported() bool { return runtime.GOOS == "darwin" }

// Enabled reports whether the user wants the icon. They do until they say
// otherwise, which is recorded as a file so that the default needs none.
func Enabled(p paths.Paths) bool {
	_, err := os.Stat(p.MenuBarOff)
	return os.IsNotExist(err)
}

// SetEnabled records whether the user wants the icon.
func SetEnabled(p paths.Paths, on bool) error {
	if on {
		if err := os.Remove(p.MenuBarOff); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := p.EnsureDirs(); err != nil {
		return err
	}
	f, err := os.OpenFile(p.MenuBarOff, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// Job is one line of the menu.
type Job struct {
	ID      int64
	Title   string
	Running bool // can be stopped; otherwise it is pending and can be run now or cancelled
}

// Config is what the icon shows and does. Every function is called on the
// main thread, one at a time.
type Config struct {
	// Jobs returns the jobs to list, in order. With show false the icon goes
	// away for good. It is called about once a second.
	Jobs func() (jobs []Job, show bool)
	// RunNow, Cancel and Stop act on a job picked from the menu.
	RunNow func(id int64)
	Cancel func(id int64)
	Stop   func(id int64)
	// Hide is "Hide Menu Bar Icon": the user does not want the icon any more.
	Hide func()
}

var errUnsupported = errors.New("the menu bar icon is only available on macOS")
