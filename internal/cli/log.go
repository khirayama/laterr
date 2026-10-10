package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/khirayama/laterr/internal/store"
)

func (a *app) logs(args []string) error {
	flags, positional, err := parseArgs("log", args, "--follow", "-f")
	if err != nil {
		return err
	}
	id, err := jobID("log", positional)
	if err != nil {
		return err
	}
	_, st, j, err := getJob(id)
	if err != nil {
		return err
	}
	defer st.Close()

	switch j.Status {
	case store.Pending:
		fmt.Fprintf(a.stderr, "Job #%d has not run yet (scheduled for %s).\n", id, when(j.ScheduledAt, time.Now(), ", ", false))
		return nil
	case store.Cancelled, store.Missed:
		fmt.Fprintf(a.stderr, "Job #%d was %s and never ran.\n", id, j.Status)
		return nil
	}

	j, printed, err := relay(st, j, a.stdout, flags["--follow"] || flags["-f"], nil, nil)
	if err != nil {
		return err
	}
	if !printed && j.ErrorMessage != "" {
		fmt.Fprintf(a.stderr, "Job #%d %s without output: %s\n", id, j.Status, j.ErrorMessage)
	}
	return nil
}

// relay copies a job's log to w. With follow it keeps copying until the job is
// no longer running, or until stop (if given) reports true. While hold (if
// given) reports true nothing is written, and relay waits, even past the end
// of the job, to deliver the rest. It returns the job as last seen and whether
// a log file existed at all.
func relay(st *store.Store, j store.Job, w io.Writer, follow bool, stop, hold func() bool) (store.Job, bool, error) {
	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	for {
		// The status was read before the log: once the job is seen as
		// finished, everything it wrote is already in the file.
		held := hold != nil && hold()
		if f == nil && !held {
			var err error
			if f, err = os.Open(j.LogPath); err != nil {
				f = nil
				if !os.IsNotExist(err) {
					return j, false, err
				}
			}
		}
		if f != nil && !held {
			if _, err := io.Copy(w, f); err != nil {
				return j, true, err
			}
		}
		if stop != nil && stop() {
			return j, f != nil, nil
		}
		if !held && (!follow || j.Status != store.Running) {
			return j, f != nil, nil
		}
		time.Sleep(200 * time.Millisecond)
		if j.Status == store.Running {
			var err error
			if j, err = st.Get(j.ID); err != nil {
				return j, f != nil, err
			}
		}
	}
}
