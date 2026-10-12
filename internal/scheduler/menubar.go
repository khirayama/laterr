package scheduler

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/khirayama/laterr/internal/menubar"
	"github.com/khirayama/laterr/internal/paths"
)

// menuBarRetry is how long a menu bar icon that failed is left alone. It
// fails where there is no menu bar to reach, such as a scheduler started by
// hand over SSH, and that does not get better by trying harder.
const menuBarRetry = 10 * time.Minute

// menuBar is the menu bar icon (see package menubar): a child process, which
// goes away by itself when there is nothing left to show.
type menuBar struct {
	paths paths.Paths

	mu      sync.Mutex
	cmd     *exec.Cmd // nil unless the icon is up
	retryAt time.Time // not before this, after a failure
}

// showMenuBar starts the menu bar icon if it is wanted, there are jobs to
// show, and it is not up already. It is called whenever the schedule may have
// changed.
func (s *scheduler) showMenuBar() {
	m := &s.menu
	// A scheduler on a data directory of its own is a test or a development
	// run, and has no business in the user's menu bar.
	if !menubar.Supported() || !paths.ServiceManaged() || !menubar.Enabled(m.paths) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil || time.Now().Before(m.retryAt) {
		return
	}
	if pending, running, err := s.store.CountActive(); err != nil || pending+running == 0 {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, "__menubar")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		s.log.Printf("menu bar: %v", err)
		m.retryAt = time.Now().Add(menuBarRetry)
		return
	}
	m.cmd = cmd
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.cmd != cmd {
			return // stopped on purpose
		}
		m.cmd = nil
		if err != nil {
			s.log.Printf("menu bar: %v", err)
			m.retryAt = time.Now().Add(menuBarRetry)
			return
		}
		// It may have left just as a job was added.
		s.notify()
	}()
}

// stop takes the icon away, with the scheduler.
func (m *menuBar) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil {
		m.cmd.Process.Signal(syscall.SIGTERM)
		m.cmd = nil
	}
}
