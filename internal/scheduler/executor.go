package scheduler

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/khirayama/laterr/internal/store"
)

// prepare builds the command for a job and opens its log file. The command
// runs directly, without a shell, in the job's directory and with exactly the
// environment captured for it. Stdin is /dev/null.
//
// Each job leads a process group of its own, so that stopping it (on shutdown
// or with `laterr kill`) also stops whatever it started. The price is that a
// scheduler killed outright cannot take its jobs along: systemd still cleans
// them up through the cgroup, launchd does not, and such a job keeps running
// while it is recorded as interrupted.
func prepare(j store.Job, env []string) (*exec.Cmd, *os.File, error) {
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
	logFile, err := os.OpenFile(j.LogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot open log file: %w", err)
	}
	cmd := &exec.Cmd{
		Path:        path,
		Args:        j.Argv,
		Dir:         j.Cwd,
		Env:         env,
		Stdout:      logFile,
		Stderr:      logFile,
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}
	if len(env) == 0 {
		cmd.Env = []string{} // nil would mean "inherit the scheduler's environment"
	}
	return cmd, logFile, nil
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
