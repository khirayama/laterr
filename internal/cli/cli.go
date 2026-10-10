// Package cli implements the laterr command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/khirayama/laterr/internal/paths"
	"github.com/khirayama/laterr/internal/scheduler"
	"github.com/khirayama/laterr/internal/store"
	"github.com/khirayama/laterr/internal/timespec"
)

const usage = `laterr - a tiny CLI to run commands later

Usage:
  laterr [options] <time> -- <command> [args...]
  laterr ls [--all] [--json]       List jobs (pending and running by default)
  laterr show <id>                 Show the details of a job
  laterr rm <id>...                Cancel pending jobs
  laterr kill <id>                 Stop a running job
  laterr log <id> [--follow]       Print the output of a job
  laterr prune [--older-than <duration> | --all]
                                   Delete finished jobs and their logs
                                   (default: finished more than 30d ago)
  laterr doctor                    Check that laterr is set up correctly
  laterr uninstall [--purge]       Remove the background service (and data)

Time:
  +10m  +2h  +1h30m  +600s  +1d    After a duration (units: d, h, m, s)
  @13:00  @13:00:30                The next time the clock shows this time
  @tomorrow/09:00                  Tomorrow at this time
  @2026-10-12/13:30                On this date at this time

Options:
  --grace <duration>   Skip the job if it would start more than this long
                       after its scheduled time, e.g. 10m or 1h30m (default 24h)
  --no-env             Pass only a minimal environment to the command
  --bg                 Do not print the output in this terminal; read it with
                       "laterr log <id>" instead
  --remind <duration>  How long before it runs to announce the job in this
                       terminal (default 5s)
  --no-remind          Do not announce the job
  --version            Print the version
  --help               Print this help

Examples:
  laterr +10m -- npm install
  laterr @tomorrow/09:00 -- ./backup.sh
  laterr +1h -- sh -c 'make build && make test'

The command runs in the background, directly (without a shell), in the
directory and with the environment you scheduled it from. Use sh -c for pipes
and redirects. Its output is shown in this terminal while it stays open, and
is always kept: see "laterr log <id>". Closing the terminal does not stop it.
`

type app struct {
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	version string
}

// usageError is a mistake in how laterr was invoked.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{fmt.Sprintf(format, args...)}
}

// errSilent makes the command exit non-zero without printing an error line,
// for commands that have already reported what is wrong.
var errSilent = errors.New("")

// Run executes laterr with the given arguments (without the program name) and
// returns the process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr, version: version}
	err := a.dispatch(args)
	if err == nil {
		return 0
	}
	if err != errSilent {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		var ue *usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(stderr, "Run `laterr --help` for usage.")
		}
	}
	return 1
}

func (a *app) dispatch(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return errSilent
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "--help", "-h", "help":
		fmt.Fprint(a.stdout, usage)
		return nil
	case "--version":
		fmt.Fprintf(a.stdout, "laterr %s\n", a.version)
		return nil
	case "ls":
		return a.list(rest)
	case "show":
		return a.show(rest)
	case "rm":
		return a.remove(rest)
	case "kill":
		return a.kill(rest)
	case "log":
		return a.logs(rest)
	case "prune":
		return a.prune(rest)
	case "doctor":
		return a.doctor(rest)
	case "uninstall":
		return a.uninstall(rest)
	case "daemon":
		return a.daemon(rest)
	case followCmd:
		return a.follow(rest)
	}
	if strings.HasPrefix(cmd, "+") || strings.HasPrefix(cmd, "@") || strings.HasPrefix(cmd, "-") {
		return a.schedule(args)
	}
	if hint := timeHint(cmd); hint != "" {
		return usagef("unknown command: %s (did you mean %s?)", cmd, hint)
	}
	return usagef("unknown command: %s", cmd)
}

// timeHint returns the time expression arg would be with its prefix, if it
// looks like one typed without it: "10m" for "+10m", "13:00" for "@13:00".
func timeHint(arg string) string {
	if _, err := timespec.ParseDuration(arg); err == nil {
		return "+" + arg
	}
	if _, err := timespec.Parse("@"+arg, time.Now(), time.Local); err == nil || errors.Is(err, timespec.ErrPast) {
		return "@" + arg
	}
	return ""
}

// daemon runs the scheduler in the foreground. The background service runs
// this; it is not meant to be typed by hand.
func (a *app) daemon(args []string) error {
	if len(args) > 0 {
		return usagef("daemon takes no arguments")
	}
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return scheduler.Run(ctx, p, a.version)
}

// parseArgs splits subcommand arguments into the given boolean flags and
// positional arguments.
func parseArgs(cmd string, args []string, flags ...string) (set map[string]bool, positional []string, err error) {
	set = map[string]bool{}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		known := false
		for _, f := range flags {
			known = known || arg == f
		}
		if !known {
			return nil, nil, usagef("unknown option for %s: %s", cmd, arg)
		}
		set[arg] = true
	}
	return set, positional, nil
}

// durationFlag reads the option name at args[*i], written either as
// "--name <duration>" or as "--name=<duration>", and moves *i past its value.
// ok is false if args[*i] is some other argument.
func durationFlag(args []string, i *int, name, example string) (text string, ok bool, err error) {
	arg := args[*i]
	if text, ok := strings.CutPrefix(arg, name+"="); ok {
		return text, true, nil
	}
	if arg != name {
		return "", false, nil
	}
	if *i+1 >= len(args) {
		return "", false, usagef("%s needs a duration, e.g. %s %s", name, name, example)
	}
	*i++
	return args[*i], true, nil
}

// parseDurationFlag parses the value given to a duration option.
func parseDurationFlag(name, text string) (time.Duration, error) {
	d, err := timespec.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s: %v", name, err)
	}
	return d, nil
}

// waitFor polls cond until it reports true or timeout has passed. cond is
// always checked at least once.
func waitFor(timeout time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(timeout); ; time.Sleep(100 * time.Millisecond) {
		if cond() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
	}
}

// jobID parses the single job ID argument of a subcommand.
func jobID(cmd string, positional []string) (int64, error) {
	if len(positional) != 1 {
		return 0, usagef("usage: laterr %s <id>", cmd)
	}
	return parseID(positional[0])
}

func parseID(arg string) (int64, error) {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return 0, usagef("invalid job ID: %s", arg)
	}
	return id, nil
}

// openStore opens the database. Unless create is set, a database that does not
// exist yet is reported as (nil, nil) instead of being created, so that
// read-only commands leave no files behind.
func openStore(p paths.Paths, create bool) (*store.Store, error) {
	if create {
		if err := p.EnsureDirs(); err != nil {
			return nil, err
		}
	} else if _, err := os.Stat(p.DB); os.IsNotExist(err) {
		return nil, nil
	}
	return store.Open(p.DB)
}

// getJob loads the job a subcommand refers to.
func getJob(id int64) (paths.Paths, *store.Store, store.Job, error) {
	p, err := paths.Resolve()
	if err != nil {
		return p, nil, store.Job{}, err
	}
	notFound := fmt.Errorf("job #%d does not exist", id)
	st, err := openStore(p, false)
	if err != nil {
		return p, nil, store.Job{}, err
	}
	if st == nil {
		return p, nil, store.Job{}, notFound
	}
	j, err := st.Get(id)
	if err != nil {
		st.Close()
		if errors.Is(err, store.ErrNotFound) {
			err = notFound
		}
		return p, nil, store.Job{}, err
	}
	return p, st, j, nil
}
