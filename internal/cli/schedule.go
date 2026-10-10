package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/khirayama/laterr/internal/paths"
	"github.com/khirayama/laterr/internal/scheduler"
	"github.com/khirayama/laterr/internal/service"
	"github.com/khirayama/laterr/internal/store"
	"github.com/khirayama/laterr/internal/timespec"
)

const (
	defaultGrace  = 24 * time.Hour
	defaultRemind = 5 * time.Second
)

type scheduleArgs struct {
	timeExpr string
	grace    time.Duration
	noEnv    bool
	bg       bool          // do not relay output to this terminal
	remind   time.Duration // notice before the job runs; 0 for none
	argv     []string
}

// parseSchedule parses `[options] <time> -- <command> [args...]`. Everything
// after the first "--" belongs to the command, untouched.
func parseSchedule(args []string) (scheduleArgs, error) {
	out := scheduleArgs{grace: defaultGrace, remind: defaultRemind}
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	// Without a separator, still scan the arguments so that the most specific
	// mistake is the one reported.
	head := args
	if sep >= 0 {
		head = args[:sep]
	}
	graceText, hasGrace := "", false
	remindText, hasRemind, noRemind := "", false, false
	for i := 0; i < len(head); i++ {
		arg := head[i]
		if text, ok, err := durationFlag(head, &i, "--grace", "10m"); err != nil {
			return out, err
		} else if ok {
			graceText, hasGrace = text, true
			continue
		}
		if text, ok, err := durationFlag(head, &i, "--remind", "30s"); err != nil {
			return out, err
		} else if ok {
			remindText, hasRemind = text, true
			continue
		}
		switch {
		case arg == "--no-env":
			out.noEnv = true
		case arg == "--bg":
			out.bg = true
		case arg == "--no-remind":
			noRemind = true
		case strings.HasPrefix(arg, "-"):
			return out, usagef("unknown option: %s", arg)
		case out.timeExpr == "":
			out.timeExpr = arg
		default:
			return out, usagef("unexpected argument: %s (did you forget `--` before the command?)", arg)
		}
	}
	if out.timeExpr == "" {
		return out, usagef("missing time (for example +10m or @13:00)")
	}
	if sep < 0 {
		return out, usagef("missing `--` before the command (usage: laterr <time> -- <command>)")
	}
	out.argv = args[sep+1:]
	if len(out.argv) == 0 {
		return out, usagef("no command given after `--`")
	}
	if out.argv[0] == "" {
		return out, usagef("the command name is empty")
	}
	if hasGrace {
		d, err := parseDurationFlag("--grace", graceText)
		if err != nil {
			return out, err
		}
		out.grace = d
	}
	if hasRemind {
		if noRemind {
			return out, usagef("--remind and --no-remind cannot be combined")
		}
		if out.bg {
			return out, usagef("--remind has no effect with --bg: there is no terminal to remind")
		}
		d, err := parseDurationFlag("--remind", remindText)
		if err != nil {
			return out, err
		}
		out.remind = d
	}
	if noRemind || out.bg {
		out.remind = 0
	}
	return out, nil
}

// minimalEnv keeps only the variables a command needs to run at all.
func minimalEnv(env []string) []string {
	keep := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"SHELL": true, "LANG": true, "TMPDIR": true,
	}
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if keep[name] || strings.HasPrefix(name, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

func (a *app) schedule(args []string) error {
	sa, err := parseSchedule(args)
	if err != nil {
		return err
	}
	// Times are interpreted here, in the CLI's time zone (which honours TZ),
	// and stored as an absolute instant. The scheduler's zone never matters.
	now := time.Now()
	at, err := timespec.Parse(sa.timeExpr, now, time.Local)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine the current directory: %v", err)
	}
	env := os.Environ()
	if sa.noEnv {
		env = minimalEnv(env)
	}

	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	if err := p.EnsureDirs(); err != nil {
		return err
	}
	// Bring the scheduler up before recording anything, so that a failure
	// here leaves no job behind.
	installed, sched, err := ensureScheduler(p)
	if err != nil {
		return err
	}
	st, err := openStore(p, true)
	if err != nil {
		return err
	}
	defer st.Close()
	job, err := st.Create(store.NewJob{
		Argv: sa.argv, Cwd: cwd, Env: env,
		CreatedAt: now, ScheduledAt: at, Grace: sa.grace,
	}, p.LogFile)
	if err != nil {
		return err
	}
	// An idle scheduler sleeps until told about new work, so the job only
	// counts as scheduled once the scheduler has heard about it.
	if err := scheduler.Wake(p.Socket); err != nil {
		st.Cancel(job.ID, time.Now())
		return err
	}

	// Output goes to this terminal only if there is one to come back to.
	attach := !sa.bg && interactive(a.stdout)
	a.printScheduled(job, now, len(env), sa.noEnv, attach, installed)
	if attach {
		if err := spawnFollower(job.ID, sa.remind); err != nil {
			fmt.Fprintf(a.stderr, "\n! Output will not be shown here (%v). Use `laterr log %d`.\n", err, job.ID)
		}
	}
	a.warnScheduled(cwd, sched)
	return nil
}

// printScheduled tells what was scheduled and, briefly, how it will behave:
// enough to know what to expect without reading the manual. installed is the
// service definition this call created, if any.
func (a *app) printScheduled(job store.Job, now time.Time, envCount int, noEnv, attach bool, installed string) {
	envLine := fmt.Sprintf("%d variables captured", envCount)
	if noEnv {
		envLine = "minimal (PATH, HOME, ...)"
	}
	at := job.ScheduledAt
	fmt.Fprintf(a.stdout, "✓ Scheduled job #%d\n\n", job.ID)
	fmt.Fprintf(a.stdout, "  Command    %s\n", commandLine(job.Argv))
	fmt.Fprintf(a.stdout, "  Run at     %s (in %s)\n", when(at, now, ", ", false), untilText(at.Sub(now)))
	fmt.Fprintf(a.stdout, "  Directory  %s\n", tildify(job.Cwd))
	fmt.Fprintf(a.stdout, "  Env        %s\n", envLine)
	fmt.Fprintf(a.stdout, "  If late    runs on wake or login, skipped after %s\n", when(job.Deadline(), now, ", ", false))
	if attach {
		fmt.Fprintf(a.stdout, "  Output     shown here while this terminal is open; always in `laterr log %d`\n", job.ID)
	} else {
		fmt.Fprintf(a.stdout, "  Output     `laterr log %d`\n", job.ID)
	}
	fmt.Fprintf(a.stdout, "\n  Runs even after this terminal is closed. Stops if you log out mid-run.\n")
	if installed != "" {
		fmt.Fprintf(a.stdout, "\n  Installed background service:\n    %s\n  Remove it anytime with `laterr uninstall`.\n", tildify(installed))
	}
}

// warnScheduled points out conditions that may keep the job from running as
// expected. Warnings never fail the command and are repeated on every schedule.
func (a *app) warnScheduled(cwd string, sched scheduler.Info) {
	if dir := protectedDir(cwd); dir != "" {
		fmt.Fprintf(a.stderr, "\n! ~/%s is protected by macOS. This job may fail or show a\n  permission dialog when it runs. See `laterr doctor`.\n", dir)
	}
	if enabled, known := service.LingerEnabled(); known && !enabled {
		fmt.Fprintf(a.stderr, "\n! This job will not run while you are logged out.\n  See `laterr doctor`.\n")
	}
	// After an upgrade the old scheduler keeps running until it is restarted.
	// It is not restarted here: that would interrupt running jobs.
	if sched.Version != a.version {
		fmt.Fprintf(a.stderr, "\n! The background scheduler is still version %s (this is %s).\n  See `laterr doctor` for how to restart it.\n", sched.Version, a.version)
	}
}

// protectedDir returns the macOS-protected folder containing dir, if any.
func protectedDir(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if name := service.ProtectedDir(runtime.GOOS, home, dir); name != "" {
		return name
	}
	// The directory may reach a protected folder through a symlink.
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		if realHome, err := filepath.EvalSymlinks(home); err == nil {
			return service.ProtectedDir(runtime.GOOS, realHome, real)
		}
	}
	return ""
}

// ensureScheduler makes sure the background scheduler is running, installing
// the OS service if needed. It returns the path of the service definition if
// this call created it, and the scheduler that answered.
func ensureScheduler(p paths.Paths) (installed string, sched scheduler.Info, err error) {
	if sched, err := scheduler.Ping(p.Socket); err == nil {
		return "", sched, nil
	}
	if !paths.ServiceManaged() {
		return "", sched, fmt.Errorf("%w (%s is set, so start it yourself with `laterr daemon`)", scheduler.ErrUnavailable, paths.HomeEnv)
	}
	binary, err := service.BinaryPath()
	if err != nil {
		return "", sched, fmt.Errorf("%w: %v", scheduler.ErrUnavailable, err)
	}
	path, created, err := service.Install(binary, p.SchedulerLog)
	if created {
		installed = path
	}
	if err != nil {
		return installed, sched, fmt.Errorf("%w: %v", scheduler.ErrUnavailable, err)
	}
	if waitFor(10*time.Second, func() bool {
		sched, err = scheduler.Ping(p.Socket)
		return err == nil
	}) {
		return installed, sched, nil
	}
	return installed, sched, errors.New(scheduler.ErrUnavailable.Error() + ": the service was started but is not responding (see `laterr doctor`)")
}
