package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/khirayama/laterr/internal/paths"
	"github.com/khirayama/laterr/internal/scheduler"
	"github.com/khirayama/laterr/internal/service"
	"github.com/khirayama/laterr/internal/store"
)

// report prints the outcome of each doctor check and counts the problems.
type report struct {
	w        io.Writer
	problems int
}

func (r *report) line(mark, label, text string) {
	fmt.Fprintf(r.w, "%s %-10s %s\n", mark, label, text)
}

func (r *report) ok(label, text string)   { r.line("✓", label, text) }
func (r *report) info(label, text string) { r.line("-", label, text) }
func (r *report) warn(label, text string) { r.line("!", label, text) }

// bad reports something that is broken; any of these makes doctor fail.
func (r *report) bad(label, text string) {
	r.problems++
	r.line("✗", label, text)
}

// hint adds advice under the line reported last.
func (r *report) hint(format string, args ...any) {
	for _, l := range strings.Split(fmt.Sprintf(format, args...), "\n") {
		fmt.Fprintf(r.w, "             %s\n", l)
	}
}

func (a *app) doctor(args []string) error {
	if len(args) > 0 {
		return usagef("doctor takes no arguments")
	}
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	r := &report{w: a.stdout}

	fmt.Fprintf(a.stdout, "laterr %s (%s/%s)\n\n", a.version, runtime.GOOS, runtime.GOARCH)

	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		r.bad("Platform", runtime.GOOS+" is not supported (macOS and Linux only)")
	}
	if exe, err := os.Executable(); err != nil {
		r.bad("Binary", err.Error())
	} else {
		r.ok("Binary", exe)
	}
	jobs := checkData(r, p)
	installed := checkService(r)
	a.checkScheduler(r, p, installed, len(jobs))
	checkTimeZone(r)
	checkJobs(r, jobs)

	if r.problems > 0 {
		fmt.Fprintf(a.stdout, "\n%d problem(s) found.\n", r.problems)
		return errSilent
	}
	fmt.Fprintln(a.stdout, "\nNo problems found.")
	return nil
}

// checkData checks the data directory and the database, and returns the
// pending and running jobs.
func checkData(r *report, p paths.Paths) []store.Job {
	fi, err := os.Stat(p.Dir)
	switch {
	case os.IsNotExist(err):
		r.info("Data", p.Dir+" (not created yet; created on first schedule)")
		return nil
	case err != nil:
		r.bad("Data", err.Error())
		return nil
	case !fi.IsDir():
		r.bad("Data", p.Dir+" is not a directory")
		return nil
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		r.bad("Data", fmt.Sprintf("%s has mode %04o; it should be 0700", p.Dir, perm))
		r.hint("Fix: chmod 700 %s", commandLine([]string{p.Dir}))
	} else if f, err := os.CreateTemp(p.Dir, ".doctor-*"); err != nil {
		r.bad("Data", p.Dir+" is not writable")
	} else {
		f.Close()
		os.Remove(f.Name())
		r.ok("Data", p.Dir)
	}

	fi, err = os.Stat(p.DB)
	if os.IsNotExist(err) {
		r.info("Database", "not created yet")
		return nil
	}
	if err != nil {
		r.bad("Database", err.Error())
		return nil
	}
	st, err := store.Open(p.DB)
	if err != nil {
		r.bad("Database", err.Error())
		return nil
	}
	defer st.Close()
	jobs, err := st.List(false)
	if err != nil {
		r.bad("Database", err.Error())
		return nil
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		r.bad("Database", fmt.Sprintf("%s has mode %04o; it should be 0600", p.DB, perm))
		r.hint("Fix: chmod 600 %s", commandLine([]string{p.DB}))
		return jobs
	}
	pending, running := 0, 0
	for _, j := range jobs {
		if j.Status == store.Pending {
			pending++
		} else {
			running++
		}
	}
	r.ok("Database", fmt.Sprintf("%d pending, %d running", pending, running))
	return jobs
}

// checkService checks the service definition and reports whether one is
// installed.
func checkService(r *report) (installed bool) {
	if !paths.ServiceManaged() {
		r.info("Service", fmt.Sprintf("not managed (%s is set; run `laterr daemon` yourself)", paths.HomeEnv))
		return false
	}
	def, err := service.DefinitionPath()
	if err != nil {
		r.bad("Service", err.Error())
		return false
	}
	if !service.Installed() {
		r.info("Service", "not installed yet (installed on first schedule)")
		return false
	}
	if bin, err := service.DefinedBinary(); err != nil {
		r.bad("Service", fmt.Sprintf("%s cannot be read: %v", def, err))
	} else if fi, err := os.Stat(bin); err != nil || fi.IsDir() {
		r.bad("Service", fmt.Sprintf("%s runs %s, which does not exist", def, bin))
		r.hint("It is repaired automatically the next time you schedule a job.")
	} else {
		r.ok("Service", fmt.Sprintf("%s (runs %s)", def, bin))
	}
	return true
}

// checkScheduler checks that the scheduler is alive by talking to its socket.
// active is the number of pending and running jobs that depend on it.
func (a *app) checkScheduler(r *report, p paths.Paths, installed bool, active int) {
	sched, err := scheduler.Ping(p.Socket)
	switch {
	case err == nil && sched.Version != a.version:
		r.warn("Scheduler", fmt.Sprintf("running (pid %d), but it is version %s and this is %s", sched.PID, sched.Version, a.version))
		if cmd := service.RestartCommand(); cmd != "" && paths.ServiceManaged() {
			r.hint("Restart it to switch over (this interrupts running jobs):\n  %s", cmd)
		}
	case err == nil:
		r.ok("Scheduler", fmt.Sprintf("running (pid %d)", sched.PID))
	case installed || active > 0:
		r.bad("Scheduler", "not running")
		if active > 0 {
			r.hint("%d job(s) are waiting and will not start until it runs.", active)
		}
		if paths.ServiceManaged() {
			r.hint("Scheduling any job starts it. If it keeps failing, see:\n  %s", service.LogHint(p.SchedulerLog))
			if cmd := service.RestartCommand(); cmd != "" && installed {
				r.hint("If it is running but stuck, restart it (this interrupts running jobs):\n  %s", cmd)
			}
		}
	default:
		r.info("Scheduler", "not running (started on first schedule)")
	}
}

func checkTimeZone(r *report) {
	zone := time.Now().Format("MST (UTC-07:00)")
	if tz, set := os.LookupEnv("TZ"); set {
		zone += fmt.Sprintf(", from TZ=%s", tz)
	}
	r.ok("Time zone", zone)
	r.hint("Times such as @13:00 are read in this zone when you schedule.")
}

// checkJobs looks for conditions that keep jobs from running although
// everything is healthy.
func checkJobs(r *report, jobs []store.Job) {
	if enabled, known := service.LingerEnabled(); known && !enabled {
		r.warn("Logout", "jobs will not run while you are logged out")
		r.hint("systemd stops your services when your last session ends.\nTo keep the scheduler running: loginctl enable-linger %s", os.Getenv("USER"))
	}
	byDir := map[string][]string{}
	for _, j := range jobs {
		if j.Status != store.Pending {
			continue
		}
		if dir := protectedDir(j.Cwd); dir != "" {
			byDir[dir] = append(byDir[dir], fmt.Sprintf("#%d", j.ID))
		} else if _, err := os.Stat(j.Cwd); os.IsNotExist(err) {
			r.warn("Job", fmt.Sprintf("#%d will fail: its directory no longer exists (%s)", j.ID, j.Cwd))
		}
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		r.warn("Privacy", fmt.Sprintf("~/%s is protected by macOS (pending: %s)", dir, strings.Join(byDir[dir], ", ")))
	}
	if len(dirs) > 0 {
		r.hint("Jobs started in the background may be denied access to these folders,\n" +
			"or macOS may ask for permission when they run. To allow it beforehand:\n" +
			"  System Settings > Privacy & Security > Full Disk Access > add laterr\n" +
			"or click Allow when the dialog appears, or keep the project elsewhere.")
	}
}
