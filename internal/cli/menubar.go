package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/khirayama/laterr/internal/menubar"
	"github.com/khirayama/laterr/internal/paths"
	"github.com/khirayama/laterr/internal/scheduler"
	"github.com/khirayama/laterr/internal/store"
)

// menubarCmd is the hidden subcommand the menu bar icon runs as. The
// scheduler starts it (see scheduler.menuBar).
const menubarCmd = "__menubar"

// menubar is `laterr menubar [on|off]`: whether pending and running jobs are
// shown in the macOS menu bar.
func (a *app) menubar(args []string) error {
	_, positional, err := parseArgs("menubar", args)
	if err != nil {
		return err
	}
	if len(positional) > 1 || (len(positional) == 1 && positional[0] != "on" && positional[0] != "off") {
		return usagef("usage: laterr menubar [on|off]")
	}
	if !menubar.Supported() {
		return fmt.Errorf("the menu bar icon is only available on macOS")
	}
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		if menubar.Enabled(p) {
			fmt.Fprintln(a.stdout, "The menu bar icon is on: it is shown while jobs are pending or running.\nTurn it off with `laterr menubar off`.")
		} else {
			fmt.Fprintln(a.stdout, "The menu bar icon is off. Turn it on with `laterr menubar on`.")
		}
		return nil
	}
	on := positional[0] == "on"
	if err := menubar.SetEnabled(p, on); err != nil {
		return err
	}
	// The scheduler starts the icon; a running icon notices by itself that it
	// is no longer wanted.
	scheduler.Wake(p.Socket)
	if on {
		fmt.Fprintln(a.stdout, "✓ Menu bar icon on (shown while jobs are pending or running)")
	} else {
		fmt.Fprintln(a.stdout, "✓ Menu bar icon off")
	}
	return nil
}

// menubarIcon is the menu bar icon: `laterr __menubar`. It lasts while it is
// wanted, there are jobs to show, and the scheduler that started it lives.
func (a *app) menubarIcon(args []string) error {
	if len(args) > 0 {
		return usagef("unknown command: %s", menubarCmd)
	}
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	st, err := openStore(p, false)
	if err != nil {
		return err
	}
	if st == nil {
		return nil // no jobs yet
	}
	defer st.Close()

	parent := os.Getppid()
	return menubar.Run(menubar.Config{
		Jobs: func() ([]menubar.Job, bool) {
			if os.Getppid() != parent || !menubar.Enabled(p) {
				return nil, false
			}
			jobs, err := st.List(false)
			if err != nil {
				return nil, true // keep what is shown; the next look may work
			}
			now := time.Now()
			out := make([]menubar.Job, len(jobs))
			for i, j := range jobs {
				at := when(j.ScheduledAt, now, " ", false)
				if j.Status == store.Running {
					at = "running"
				}
				out[i] = menubar.Job{
					ID:      j.ID,
					Title:   fmt.Sprintf("#%d   %s   %s", j.ID, at, oneLine(commandLine(j.Argv), 40)),
					Running: j.Status == store.Running,
				}
			}
			return out, len(out) > 0
		},
		// Failures have nowhere to be shown: the job stays in the menu.
		RunNow: func(id int64) {
			if st.RunNow(id, time.Now()) == nil {
				scheduler.Wake(p.Socket)
			}
		},
		Cancel: func(id int64) {
			if st.Cancel(id, time.Now()) == nil {
				scheduler.Wake(p.Socket)
			}
		},
		Stop: func(id int64) { scheduler.Kill(p.Socket, id) },
		Hide: func() { menubar.SetEnabled(p, false) },
	})
}
