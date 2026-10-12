package scheduler

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/khirayama/laterr/internal/store"
)

// drainGrace bounds each wait for a finished job's terminal output to reach
// its log.
const drainGrace = time.Second

// output is where a job writes: its log file. A job whose output is shown in
// the terminal it was scheduled from writes to a terminal of its own instead,
// of the same size, and what it writes there is copied to the log. Commands
// that draw colors or progress bars only on a terminal then behave as they do
// when run by hand, and replaying the log on the real terminal looks the same.
// The follower reports when the real terminal changes size (see resize).
type output struct {
	log           *os.File
	master, slave *os.File      // nil without a terminal
	copied        chan struct{} // closed once the terminal has been copied to the end
}

func openOutput(j store.Job) (*output, error) {
	logFile, err := os.OpenFile(j.LogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot open log file: %w", err)
	}
	o := &output{log: logFile}
	if j.TTYCols <= 0 || j.TTYRows <= 0 {
		return o, nil
	}
	// Without a terminal the job still runs, only plainer.
	master, slave, err := pty.Open()
	if err != nil {
		return o, nil
	}
	pty.Setsize(slave, &pty.Winsize{Cols: uint16(j.TTYCols), Rows: uint16(j.TTYRows)})
	// A terminal turns each newline into CR LF on the way out. The log keeps
	// what the job wrote; the terminal it is replayed on does that itself.
	if t, err := unix.IoctlGetTermios(int(slave.Fd()), ioctlGetTermios); err == nil {
		t.Oflag &^= unix.ONLCR
		unix.IoctlSetTermios(int(slave.Fd()), ioctlSetTermios, t)
	}
	o.master, o.slave = master, slave
	return o, nil
}

// file is what the job gets as stdout and stderr.
func (o *output) file() *os.File {
	if o.slave != nil {
		return o.slave
	}
	return o.log
}

// resize changes the size of the job's terminal, if it has one.
func (o *output) resize(cols, rows int) bool {
	if o.master == nil || cols <= 0 || rows <= 0 || cols > 0xffff || rows > 0xffff {
		return false
	}
	return pty.Setsize(o.master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) == nil
}

// started begins copying the job's terminal to its log.
func (o *output) started() {
	if o.master == nil {
		return
	}
	o.copied = make(chan struct{})
	go func() {
		defer close(o.copied)
		// Ends when nothing has the terminal open any more: with EOF or,
		// on Linux, an error.
		io.Copy(o.log, o.master)
		o.master.Close()
		o.log.Close()
	}()
}

// close is called when the job has exited, or could not be started. On return
// the job's output is in the log.
func (o *output) close() {
	if o.copied == nil {
		o.log.Close()
		if o.master != nil {
			o.slave.Close()
			o.master.Close()
		}
		return
	}
	// Closing the last handle on a terminal can discard what has not been
	// read from it yet, so let the copy catch up first.
	for deadline := time.Now().Add(drainGrace); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if n, err := unix.IoctlGetInt(int(o.slave.Fd()), unix.TIOCOUTQ); err != nil || n == 0 {
			break
		}
	}
	o.slave.Close()
	// Something the job left behind may keep the terminal open. Its output
	// still reaches the log, later.
	select {
	case <-o.copied:
	case <-time.After(drainGrace):
	}
}

// prepare builds the command for a job and opens its output. The command
// runs directly, without a shell, in the job's directory and with exactly the
// environment captured for it. Stdin is /dev/null.
//
// Each job leads a process group of its own, so that stopping it (on shutdown
// or with `laterr kill`) also stops whatever it started. The price is that a
// scheduler killed outright cannot take its jobs along: systemd still cleans
// them up through the cgroup, launchd does not, and such a job keeps running
// while it is recorded as interrupted.
func prepare(j store.Job, env []string) (*exec.Cmd, *output, error) {
	if len(j.Argv) == 0 {
		return nil, nil, errors.New("empty command")
	}
	if fi, err := os.Stat(j.Cwd); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("working directory does not exist: %s", j.Cwd)
		}
		return nil, nil, fmt.Errorf("working directory is not accessible: %s: %w", j.Cwd, unwrapPathError(err))
	} else if !fi.IsDir() {
		return nil, nil, fmt.Errorf("working directory is not a directory: %s", j.Cwd)
	}
	path, err := lookPath(j.Argv[0], j.Cwd, env)
	if err != nil {
		return nil, nil, err
	}
	out, err := openOutput(j)
	if err != nil {
		return nil, nil, err
	}
	cmd := &exec.Cmd{
		Path:        path,
		Args:        j.Argv,
		Dir:         j.Cwd,
		Env:         env,
		Stdout:      out.file(),
		Stderr:      out.file(),
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}
	if len(env) == 0 {
		cmd.Env = []string{} // nil would mean "inherit the scheduler's environment"
	}
	return cmd, out, nil
}

// lookPath finds the executable for a job the way a shell started in cwd with
// env would. exec.LookPath cannot be used: it consults the scheduler's own
// PATH and working directory, not the job's.
func lookPath(name, cwd string, env []string) (string, error) {
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(cwd, p)
	}
	if strings.Contains(name, "/") {
		if err := executable(abs(name)); err != nil {
			return "", fmt.Errorf("cannot run %s: %w", name, err)
		}
		return abs(name), nil
	}
	pathVar, ok := "", false
	for _, kv := range env {
		if v, found := strings.CutPrefix(kv, "PATH="); found {
			pathVar, ok = v, true // the last entry wins, as in execve
		}
	}
	if !ok {
		return "", fmt.Errorf("command not found: %s (the job's environment has no PATH)", name)
	}
	for _, dir := range filepath.SplitList(pathVar) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(abs(dir), name)
		if executable(p) == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("command not found: %s", name)
}

func executable(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return unwrapPathError(err)
	}
	if fi.IsDir() {
		return errors.New("is a directory")
	}
	if fi.Mode().Perm()&0o111 == 0 {
		return errors.New("permission denied")
	}
	return nil
}

// unwrapPathError drops the "stat /path:" prefix when the caller already names
// the path.
func unwrapPathError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// outcome maps a finished process to a job status. A non-zero exit is failed,
// and so is death by signal, which has no exit code.
func outcome(state *os.ProcessState, waitErr error) (store.Status, *int, string) {
	if state == nil {
		return store.Failed, nil, fmt.Sprintf("waiting for the process failed: %v", waitErr)
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return store.Failed, nil, fmt.Sprintf("terminated by signal: %s", ws.Signal())
	}
	code := state.ExitCode()
	if code == 0 {
		return store.Succeeded, &code, ""
	}
	return store.Failed, &code, ""
}
