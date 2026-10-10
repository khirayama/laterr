package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/khirayama/laterr/internal/store"
)

// Every job is run by the background scheduler. By default the terminal a job
// was scheduled from also gets a follower: a small laterr process that stays
// behind after the prompt returns, announces the job shortly before it runs,
// and copies the job's output to the terminal. The follower only relays. If
// the terminal is closed it goes away and the job runs regardless, with its
// output in the log as always.
//
// It writes only while the terminal is at a shell prompt (see atPrompt). While
// something else is in the foreground it keeps quiet and catches up afterwards.

// followCmd is the hidden subcommand the follower runs as.
const followCmd = "__follow"

// maxNap bounds each sleep while waiting, for the same reason as the
// scheduler's: timers stop while the machine sleeps, the wall clock does not.
const maxNap = 30 * time.Second

// interactive reports whether w is a terminal that a follower could write to
// later. Output captured by a pipe or a file gets no follower: it would hold
// that pipe open until the job has run.
func interactive(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0 && hasTerminal()
}

// spawnFollower starts the follower for a job and leaves it running.
func spawnFollower(id int64, remind time.Duration) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// It keeps the terminal for output only; stdin is /dev/null.
	cmd := &exec.Cmd{
		Path: self,
		Args: []string{self, followCmd, strconv.FormatInt(id, 10),
			strconv.FormatInt(int64(remind/time.Second), 10), strconv.Itoa(parentGroup())},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// followArgs parses the follower's arguments:
// `<id> <remind-seconds> [<shell-pgid>]`.
func followArgs(args []string) (id int64, remind time.Duration, shell int, ok bool) {
	if len(args) != 2 && len(args) != 3 {
		return 0, 0, 0, false
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	seconds, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	if len(args) == 3 {
		if shell, err = strconv.Atoi(args[2]); err != nil {
			return 0, 0, 0, false
		}
	}
	return id, time.Duration(seconds) * time.Second, shell, true
}

// follow is the follower: `laterr __follow <id> <remind-seconds> [<shell-pgid>]`.
func (a *app) follow(args []string) error {
	id, remind, shell, ok := followArgs(args)
	if !ok {
		return usagef("unknown command: %s", followCmd)
	}
	_, st, j, err := getJob(id)
	if err != nil {
		return errSilent // the job is gone; nothing to relay
	}
	defer st.Close()

	// Closing the terminal does not signal this process (the shell does not
	// know it as a job), so watch for the terminal going away instead.
	attached := hasTerminal()
	gone := func() bool { return attached && !hasTerminal() }
	busy := func() bool { return !atPrompt(shell) }
	// After writing below the shell's prompt, have the shell draw it again.
	prompt := func() {
		if interactive(a.stderr) && !busy() {
			redrawPrompt()
		}
	}

	// A reminder is pointless when the job is due sooner than the notice.
	reminded := remind <= 0 || time.Until(j.ScheduledAt) <= remind
	for j.Status == store.Pending {
		if gone() {
			return nil
		}
		left := time.Until(j.ScheduledAt)
		if !reminded && left <= remind {
			reminded = true
			// A notice that cannot be shown now is useless later.
			if left > 0 && !busy() {
				fmt.Fprintf(a.stderr, "laterr: job #%d (%s) runs in %s\n", id, shortCommand(j.Argv), untilText(left))
				prompt()
			}
		}
		var nap time.Duration
		switch {
		case !reminded:
			nap = left - remind
		case left > 0:
			nap = left
		case left > -10*time.Second:
			nap = 100 * time.Millisecond // due: the scheduler starts it any moment
		default:
			nap = 5 * time.Second // overdue: the scheduler is not running
		}
		time.Sleep(min(nap, maxNap))
		if j, err = st.Get(id); err != nil {
			return errSilent
		}
	}

	switch j.Status {
	case store.Cancelled:
		return nil
	case store.Missed:
		fmt.Fprintf(a.stderr, "laterr: job #%d (%s) was missed and did not run\n", id, shortCommand(j.Argv))
		prompt()
		return nil
	}
	j, _, err = relay(st, j, a.stdout, true, gone, busy)
	if err != nil {
		return errSilent
	}
	if line := resultLine(j); line != "" {
		fmt.Fprintln(a.stderr, line)
	}
	prompt()
	return nil
}

// resultLine says how a job ended: the exit code is otherwise invisible in the
// terminal, and a command that prints nothing would leave no sign of having
// run.
func resultLine(j store.Job) string {
	switch {
	case j.Status == store.Succeeded:
		return fmt.Sprintf("laterr: job #%d succeeded (%s)", j.ID, elapsed(j.FinishedAt.Sub(j.StartedAt)))
	case j.Status == store.Failed && j.ExitCode != nil && j.ErrorMessage != "":
		return fmt.Sprintf("laterr: job #%d failed (exit code %d): %s", j.ID, *j.ExitCode, j.ErrorMessage)
	case j.Status == store.Failed && j.ExitCode != nil:
		return fmt.Sprintf("laterr: job #%d failed (exit code %d)", j.ID, *j.ExitCode)
	case j.Status == store.Failed || j.Status == store.Interrupted:
		return fmt.Sprintf("laterr: job #%d %s: %s", j.ID, j.Status, j.ErrorMessage)
	}
	return ""
}
