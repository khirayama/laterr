package main_test

// End-to-end tests: they build the real binary, run the real scheduler against
// a throwaway data directory (LATERR_HOME), and drive it through the CLI. The
// OS service is never touched; the scheduler is started as a plain process.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "laterr-bin-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "laterr")
	args := []string{"build", "-o", binary}
	cgo := "CGO_ENABLED=0"
	if raceEnabled {
		args = append(args, "-race")
		cgo = "CGO_ENABLED=1"
	}
	build := exec.Command("go", append(args, ".")...)
	build.Env = append(os.Environ(), cgo)
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type job struct {
	ID           int64      `json:"id"`
	Status       string     `json:"status"`
	Command      []string   `json:"command"`
	Cwd          string     `json:"cwd"`
	ScheduledAt  time.Time  `json:"scheduled_at"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	PID          *int       `json:"pid"`
	ExitCode     *int       `json:"exit_code"`
	ErrorMessage *string    `json:"error_message"`
	LogPath      string     `json:"log_path"`
}

type sandbox struct {
	t      *testing.T
	home   string // LATERR_HOME
	work   string // directory commands are scheduled from
	extra  []string
	daemon *exec.Cmd
	log    *lockedBuffer
}

// exec.Cmd copies stderr in a goroutine while tests inspect it for recovery
// messages. bytes.Buffer alone is not safe for those concurrent accesses.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	t.Parallel()
	// Not t.TempDir(): its long paths can exceed the Unix socket path limit.
	root, err := os.MkdirTemp("/tmp", "laterr-it-")
	if err != nil {
		t.Fatal(err)
	}
	s := &sandbox{t: t, home: filepath.Join(root, "home"), work: filepath.Join(root, "my work")}
	if err := os.Mkdir(s.work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.stopDaemon(syscall.SIGKILL)
		if t.Failed() && s.log != nil {
			t.Logf("scheduler log:\n%s", s.log)
		}
		os.RemoveAll(root)
	})
	return s
}

func (s *sandbox) env() []string {
	env := append(os.Environ(), "LATERR_HOME="+s.home, "TZ=UTC")
	if raceEnabled {
		// Race-instrumented CLI calls are short-lived and frequently polled.
		// Keep reports enabled without sleeping a second on every clean exit.
		env = append(env, "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0 exitcode=66")
	}
	return append(env, s.extra...)
}

func (s *sandbox) startDaemon() {
	s.t.Helper()
	if s.log == nil {
		s.log = &lockedBuffer{}
	}
	s.daemon = exec.Command(binary, "daemon")
	s.daemon.Env = s.env()
	s.daemon.Stderr = s.log
	if err := s.daemon.Start(); err != nil {
		s.t.Fatal(err)
	}
	sock := filepath.Join(s.home, "laterr.sock")
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return
		}
	}
	s.t.Fatal("scheduler did not come up")
}

// stopDaemon signals the scheduler and waits for it to exit.
func (s *sandbox) stopDaemon(sig syscall.Signal) {
	if s.daemon == nil {
		return
	}
	s.daemon.Process.Signal(sig)
	s.daemon.Wait()
	if raceEnabled && s.daemon.ProcessState.ExitCode() == 66 {
		s.t.Errorf("scheduler data race:\n%s", s.log)
	}
	s.daemon = nil
}

// run executes the CLI from the work directory.
func (s *sandbox) run(args ...string) (stdout, stderr string, code int) {
	return s.runIn(s.work, "", args...)
}

func (s *sandbox) runIn(dir, stdin string, args ...string) (stdout, stderr string, code int) {
	s.t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = s.env()
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			s.t.Fatalf("laterr %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errOut.String(), code
}

// ok runs the CLI and fails the test if it exits non-zero.
func (s *sandbox) ok(args ...string) string {
	s.t.Helper()
	out, errOut, code := s.run(args...)
	if code != 0 {
		s.t.Fatalf("laterr %v: exit %d\n%s%s", args, code, out, errOut)
	}
	return out
}

func (s *sandbox) jobs() []job {
	s.t.Helper()
	var jobs []job
	out := s.ok("ls", "--all", "--json")
	if err := json.Unmarshal([]byte(out), &jobs); err != nil {
		s.t.Fatalf("ls --json is not valid JSON: %v\n%s", err, out)
	}
	return jobs
}

func (s *sandbox) job(id int64) job {
	s.t.Helper()
	for _, j := range s.jobs() {
		if j.ID == id {
			return j
		}
	}
	s.t.Fatalf("job #%d not listed", id)
	return job{}
}

// wait blocks until the job reaches the given status.
func (s *sandbox) wait(id int64, status string) job {
	s.t.Helper()
	var j job
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if j = s.job(id); j.Status == status {
			return j
		}
	}
	s.t.Fatalf("job #%d is %s, want %s", id, j.Status, status)
	return j
}

func (s *sandbox) readLog(j job) string {
	s.t.Helper()
	data, err := os.ReadFile(j.LogPath)
	if err != nil {
		s.t.Fatal(err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func contains(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("%s lacks %q:\n%s", what, want, got)
		}
	}
}

func TestRunsOnTimeWithDirectoryArgsAndEnv(t *testing.T) {
	s := newSandbox(t)
	s.extra = []string{"LATERR_TEST_SECRET=hunter2-xyz"}
	s.startDaemon()

	script := `printf '%s|' "$@"; echo; pwd -P; echo "secret=$LATERR_TEST_SECRET"; cat; echo oops >&2`
	out, errOut, code := s.run("+2s", "--", "sh", "-c", script, "_", "a  b", "$HOME", "*", "it's", "", "--", "-x")
	// Stderr may only carry "!" warnings (e.g. about lingering on Linux).
	if code != 0 || (errOut != "" && !strings.HasPrefix(errOut, "\n! ")) {
		t.Fatalf("exit %d, stderr %q\n%s", code, errOut, out)
	}
	contains(t, "schedule output", out,
		"✓ Scheduled job #1",
		"Run at     Today, ", "(in 2 seconds)",
		"Directory  ",
		" variables captured",
		"If late    runs on wake or login, skipped after Tomorrow, ",
		"Runs even after this terminal is closed. Stops if you log out mid-run.")

	j := s.wait(1, "succeeded")
	if late := j.StartedAt.Sub(j.ScheduledAt); late < 0 || late > 2*time.Second {
		t.Errorf("started %v after the scheduled time", late)
	}
	if j.ExitCode == nil || *j.ExitCode != 0 {
		t.Errorf("exit code: %v", j.ExitCode)
	}
	realWork, _ := filepath.EvalSymlinks(s.work)
	if got, want := s.readLog(j), "a  b|$HOME|*|it's||--|-x|\n"+realWork+"\nsecret=hunter2-xyz\noops\n"; got != want {
		t.Errorf("log:\n got %q\nwant %q", got, want)
	}
	if fi, _ := os.Stat(j.LogPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v, want 0600", fi.Mode().Perm())
	}

	// The captured environment never shows up in any output.
	show := s.ok("show", "1")
	contains(t, "show", show, "Job #1", "Status     succeeded", "Exit code  0", "Duration", "Started", j.LogPath)
	for name, text := range map[string]string{"show": show, "ls": s.ok("ls", "--all"), "ls --json": s.ok("ls", "--all", "--json"), "schedule": out} {
		if strings.Contains(text, "hunter2") {
			t.Errorf("%s leaks the environment:\n%s", name, text)
		}
	}
	if got := s.ok("log", "1"); got != s.readLog(j) {
		t.Errorf("log command printed %q", got)
	}
	if out, _, code := s.run("doctor"); code != 0 || !strings.Contains(out, "Scheduler  running") {
		t.Errorf("doctor: exit %d\n%s", code, out)
	}
}

func TestNoEnv(t *testing.T) {
	s := newSandbox(t)
	s.extra = []string{"LATERR_TEST_SECRET=hunter2-xyz", "LC_LATERR=kept"}
	s.startDaemon()

	out := s.ok("--no-env", "+1s", "--", "env")
	contains(t, "schedule output", out, "Env        minimal (PATH, HOME, ...)")
	log := s.readLog(s.wait(1, "succeeded"))
	contains(t, "environment", log, "PATH=", "HOME=", "LC_LATERR=kept")
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		name, _, _ := strings.Cut(line, "=")
		switch name {
		case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "TMPDIR":
		default:
			if !strings.HasPrefix(name, "LC_") {
				t.Errorf("unexpected variable in minimal environment: %s", name)
			}
		}
	}
}

func TestFailures(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()

	s.ok("+1s", "--", "sh", "-c", "echo before; exit 7")
	s.ok("+1s", "--", "laterr-no-such-command")
	gone := filepath.Join(filepath.Dir(s.work), "gone")
	os.Mkdir(gone, 0o755)
	if _, errOut, code := s.runIn(gone, "", "+1s", "--", "true"); code != 0 {
		t.Fatal(errOut)
	}
	os.Remove(gone)

	j := s.wait(1, "failed")
	if j.ExitCode == nil || *j.ExitCode != 7 || s.readLog(j) != "before\n" {
		t.Errorf("job 1: %+v", j)
	}
	j = s.wait(2, "failed")
	if j.ExitCode != nil || j.ErrorMessage == nil || !strings.Contains(*j.ErrorMessage, "command not found: laterr-no-such-command") {
		t.Errorf("job 2: %+v", j)
	}
	j = s.wait(3, "failed")
	if j.ErrorMessage == nil || !strings.Contains(*j.ErrorMessage, "working directory does not exist") {
		t.Errorf("job 3: %+v", j)
	}
	if _, errOut, _ := s.run("log", "3"); !strings.Contains(errOut, "working directory does not exist") {
		t.Errorf("log 3: %q", errOut)
	}
}

func TestCancel(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()
	marker := filepath.Join(s.work, "ran")

	s.ok("+2s", "--", "touch", marker)
	if _, errOut, _ := s.run("log", "1"); !strings.Contains(errOut, "has not run yet") {
		t.Errorf("log of a pending job: %q", errOut)
	}
	contains(t, "rm", s.ok("rm", "1"), "Cancelled job #1")
	if _, errOut, code := s.run("rm", "1"); code == 0 || errOut != "Error: job #1 is already cancelled\n" {
		t.Errorf("second rm: exit %d, %q", code, errOut)
	}
	if _, errOut, code := s.run("rm", "42"); code == 0 || errOut != "Error: job #42 does not exist\n" {
		t.Errorf("rm 42: exit %d, %q", code, errOut)
	}
	if out := s.ok("ls"); strings.Contains(out, "touch") {
		t.Errorf("cancelled job still listed:\n%s", out)
	}
	contains(t, "ls --all", s.ok("ls", "--all"), "cancelled")

	time.Sleep(3500 * time.Millisecond)
	if exists(marker) {
		t.Error("the cancelled job ran")
	}
	if j := s.job(1); j.Status != "cancelled" || j.StartedAt != nil {
		t.Errorf("job 1: %+v", j)
	}
}

func TestRunNow(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()

	s.ok("+1h", "--", "sh", "-c", "echo early")
	s.ok("+1h", "--", "true")
	s.ok("+1h", "--", "true")
	s.ok("rm", "3")
	// A bad ID starts nothing.
	if _, _, code := s.run("run", "1", "x"); code == 0 || s.job(1).Status != "pending" {
		t.Errorf("run with an invalid ID: exit %d, job 1 %s", code, s.job(1).Status)
	}
	before := time.Now()
	if out := s.ok("run", "1"); out != "✓ Started job #1\n" {
		t.Errorf("run 1: %q", out)
	}
	j := s.wait(1, "succeeded")
	if got := s.readLog(j); got != "early\n" {
		t.Errorf("log: %q", got)
	}
	// It ran now, and is recorded as having been scheduled for now.
	if at := j.ScheduledAt; at.Before(before.Add(-time.Second)) || at.After(time.Now()) {
		t.Errorf("scheduled_at: %s", at)
	}
	// Each job is handled on its own; any failure makes the exit code non-zero.
	out, errOut, code := s.run("run", "1", "2", "3", "9")
	if code == 0 || out != "✓ Started job #2\n" ||
		errOut != "Error: job #1 cannot be run (it has succeeded)\nError: job #3 cannot be run (it was cancelled)\nError: job #9 does not exist\n" {
		t.Errorf("run 1 2 3 9: exit %d, %q, %q", code, out, errOut)
	}
	s.wait(2, "succeeded")
	if _, errOut, code := s.run("rm", "1"); code == 0 || errOut != "Error: job #1 cannot be cancelled (it has succeeded)\n" {
		t.Errorf("rm 1: exit %d, %q", code, errOut)
	}
}

func TestLongJobDoesNotDelayOthersAndStopsWithScheduler(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()

	s.ok("+1s", "--", "sleep", "60")
	s.ok("+2s", "--", "echo", "quick")
	quick := s.wait(2, "succeeded")
	if late := quick.StartedAt.Sub(quick.ScheduledAt); late > 2*time.Second {
		t.Errorf("second job started %v late", late)
	}
	long := s.job(1)
	if long.Status != "running" || long.PID == nil {
		t.Fatalf("job 1: %+v", long)
	}
	contains(t, "show", s.ok("show", "1"), fmt.Sprintf("PID        %d", *long.PID))
	contains(t, "ls", s.ok("ls"), "running")
	if _, errOut, code := s.run("rm", "1"); code == 0 || errOut != "Error: job #1 is already running (stop it with `laterr kill 1`)\n" {
		t.Errorf("rm of a running job: exit %d, %q", code, errOut)
	}

	// Stopping the scheduler takes the job down with it.
	start := time.Now()
	s.stopDaemon(syscall.SIGTERM)
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("shutdown took %v", d)
	}
	if j := s.job(1); j.Status != "interrupted" || j.FinishedAt == nil {
		t.Errorf("job 1 after shutdown: %+v", j)
	}
	if err := syscall.Kill(*long.PID, 0); err == nil {
		t.Errorf("job process %d survived the scheduler", *long.PID)
	}
}

func TestKill(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()
	pidFile := filepath.Join(s.work, "child.pid")

	// The job starts a child of its own; stopping the job must stop that too.
	s.ok("+1s", "--", "sh", "-c", "sleep 60 & echo $! > child.pid; wait")
	s.ok("+1h", "--", "true")
	if _, errOut, code := s.run("kill", "2"); code == 0 || errOut != "Error: job #2 has not started yet (cancel it with `laterr rm 2`)\n" {
		t.Errorf("kill of a pending job: exit %d, %q", code, errOut)
	}
	s.wait(1, "running")
	for i := 0; i < 100 && !exists(pidFile); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	data, _ := os.ReadFile(pidFile)
	child, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("child pid: %q", data)
	}

	start := time.Now()
	if out := s.ok("kill", "1"); out != "✓ Stopped job #1\n" {
		t.Errorf("kill: %q", out)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("kill took %v", d)
	}
	j := s.job(1)
	if j.Status != "failed" || j.ErrorMessage == nil || *j.ErrorMessage != "stopped with `laterr kill`" || j.FinishedAt == nil {
		t.Errorf("job 1 after kill: %+v", j)
	}
	for i := 0; i < 40 && syscall.Kill(child, 0) == nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(child, 0) == nil {
		syscall.Kill(child, syscall.SIGKILL)
		t.Errorf("the job's child process %d survived", child)
	}
	if _, errOut, code := s.run("kill", "1"); code == 0 || errOut != "Error: job #1 is not running (it has failed)\n" {
		t.Errorf("kill of a finished job: exit %d, %q", code, errOut)
	}
	// The scheduler is unharmed and still runs other jobs.
	s.ok("+1s", "--", "true")
	s.wait(3, "succeeded")
}

func TestRemoveSeveral(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()
	for i := 0; i < 3; i++ {
		s.ok("+1h", "--", "true")
	}
	// A bad ID cancels nothing.
	if _, _, code := s.run("rm", "1", "x"); code == 0 || s.job(1).Status != "pending" {
		t.Errorf("rm with an invalid ID: exit %d, job 1 %s", code, s.job(1).Status)
	}
	if out := s.ok("rm", "1", "2"); out != "✓ Cancelled job #1\n✓ Cancelled job #2\n" {
		t.Errorf("rm 1 2: %q", out)
	}
	// Each job is handled on its own; any failure makes the exit code non-zero.
	out, errOut, code := s.run("rm", "2", "3", "9")
	if code == 0 || out != "✓ Cancelled job #3\n" || errOut != "Error: job #2 is already cancelled\nError: job #9 does not exist\n" {
		t.Errorf("rm 2 3 9: exit %d, %q, %q", code, out, errOut)
	}
}

func TestPrune(t *testing.T) {
	s := newSandbox(t)
	if out := s.ok("prune"); !strings.HasPrefix(out, "Nothing to prune") || exists(s.home) {
		t.Errorf("prune without data: %q", out)
	}
	s.startDaemon()
	s.ok("+1s", "--", "echo", "hello")
	s.ok("+1h", "--", "true")
	done := s.wait(1, "succeeded")
	if !exists(done.LogPath) {
		t.Fatal("no log to prune")
	}
	// Too recent for the default, and for an explicit age.
	if out := s.ok("prune"); !strings.Contains(out, "no jobs finished more than 30d ago") {
		t.Errorf("prune: %q", out)
	}
	if out := s.ok("prune", "--older-than=1h"); !strings.Contains(out, "more than 1h ago") || !exists(done.LogPath) {
		t.Errorf("prune --older-than=1h: %q", out)
	}
	time.Sleep(1100 * time.Millisecond)
	if out := s.ok("prune", "--older-than", "1s"); out != "✓ Pruned 1 finished job and its log.\n" {
		t.Errorf("prune --older-than 1s: %q", out)
	}
	if exists(done.LogPath) {
		t.Error("the log was left behind")
	}
	// The pending job is untouched, whatever the cutoff.
	if out := s.ok("prune", "--all"); out != "Nothing to prune: no finished jobs.\n" {
		t.Errorf("prune --all: %q", out)
	}
	if jobs := s.jobs(); len(jobs) != 1 || jobs[0].ID != 2 || jobs[0].Status != "pending" {
		t.Errorf("jobs after pruning: %+v", jobs)
	}
}

func TestCrashRecovery(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()
	count := filepath.Join(s.work, "count")
	late := filepath.Join(s.work, "late")

	s.ok("+1s", "--", "sh", "-c", "echo run >> "+"count"+"; exec sleep 60")
	s.ok("+3s", "--", "touch", late)
	running := s.wait(1, "running")
	for i := 0; i < 100 && !exists(count); i++ {
		time.Sleep(50 * time.Millisecond)
	}

	// The scheduler dies without a chance to clean up; job 1 is left marked
	// running and job 2 comes due while nothing is watching.
	s.stopDaemon(syscall.SIGKILL)
	if running.PID != nil {
		syscall.Kill(*running.PID, syscall.SIGKILL) // launchd/systemd would do this
	}
	time.Sleep(time.Until(s.job(2).ScheduledAt.Add(1500 * time.Millisecond)))
	if exists(late) {
		t.Fatal("job 2 ran without a scheduler")
	}
	if j := s.job(1); j.Status != "running" {
		t.Fatalf("job 1 before restart: %s", j.Status)
	}

	restarted := time.Now()
	s.startDaemon()
	// Job 2 was restored from disk and runs late, promptly after the restart.
	j2 := s.wait(2, "succeeded")
	if !exists(late) || j2.StartedAt.Before(restarted.Add(-time.Second)) || j2.StartedAt.Sub(restarted) > 5*time.Second {
		t.Errorf("job 2: started %v, restarted %v", j2.StartedAt, restarted)
	}
	// Job 1 is recorded as interrupted and is not run a second time.
	if j := s.job(1); j.Status != "interrupted" || j.ErrorMessage == nil {
		t.Errorf("job 1 after restart: %+v", j)
	}
	time.Sleep(time.Second)
	if data, _ := os.ReadFile(count); string(data) != "run\n" {
		t.Errorf("job 1 ran %d times", strings.Count(string(data), "run"))
	}
	contains(t, "scheduler log", s.log.String(), "marked 1 job(s) from a previous run as interrupted")
}

func TestMissedAfterGrace(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()
	missed := filepath.Join(s.work, "missed")
	inTime := filepath.Join(s.work, "in-time")

	out := s.ok("--grace", "1s", "+1s", "--", "touch", missed)
	contains(t, "schedule output", out, "skipped after Today, ")
	s.ok("--grace=1h", "+1s", "--", "touch", inTime)
	s.stopDaemon(syscall.SIGTERM)
	time.Sleep(3500 * time.Millisecond)
	s.startDaemon()

	j := s.wait(1, "missed")
	s.wait(2, "succeeded")
	if exists(missed) || j.StartedAt != nil {
		t.Errorf("the missed job ran: %+v", j)
	}
	if !exists(inTime) {
		t.Error("the job within its grace period did not run")
	}
	if _, errOut, _ := s.run("log", "1"); !strings.Contains(errOut, "was missed and never ran") {
		t.Errorf("log of a missed job: %q", errOut)
	}
	contains(t, "show", s.ok("show", "1"), "Status     missed", "Grace      1s")
}

func TestConcurrentScheduling(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()
	const n = 20
	var wg sync.WaitGroup
	var failed sync.Map
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, errOut, code := s.run("+1h", "--", "echo", fmt.Sprint(i)); code != 0 {
				failed.Store(i, errOut)
			}
		}()
	}
	wg.Wait()
	failed.Range(func(k, v any) bool {
		t.Errorf("schedule %v failed: %v", k, v)
		return true
	})
	jobs := s.jobs()
	if len(jobs) != n {
		t.Fatalf("%d jobs, want %d", len(jobs), n)
	}
	args := map[string]bool{}
	for i, j := range jobs {
		if j.ID != int64(i+1) || j.Status != "pending" {
			t.Errorf("job at %d: %+v", i, j)
		}
		args[j.Command[1]] = true
	}
	if len(args) != n {
		t.Errorf("%d distinct commands, want %d", len(args), n)
	}
}

func TestLogFollow(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()
	s.ok("+1s", "--", "sh", "-c", "echo one; sleep 2; echo two")
	s.wait(1, "running")
	start := time.Now()
	if got := s.ok("log", "1", "--follow"); got != "one\ntwo\n" {
		t.Errorf("followed log: %q", got)
	}
	if d := time.Since(start); d < time.Second {
		t.Errorf("--follow returned after %v, before the job finished", d)
	}
	if j := s.job(1); j.Status != "succeeded" {
		t.Errorf("job 1: %s", j.Status)
	}
}

func TestSchedulerUnavailable(t *testing.T) {
	s := newSandbox(t)
	_, errOut, code := s.run("+1s", "--", "true")
	if code == 0 || !strings.HasPrefix(errOut, "Error: background scheduler is unavailable") {
		t.Errorf("exit %d, %q", code, errOut)
	}
	// Nothing was recorded for the failed attempt.
	if out := s.ok("ls", "--all", "--json"); strings.TrimSpace(out) != "[]" {
		t.Errorf("jobs after a failed schedule: %s", out)
	}
}

func TestUninstallKeepsOrPurgesData(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()
	s.ok("+1h", "--", "true")
	s.stopDaemon(syscall.SIGTERM)

	// Pending jobs: confirmation is required, and "no" changes nothing.
	out, _, code := s.runIn(s.work, "n\n", "uninstall", "--purge")
	if code == 0 || !strings.Contains(out, "You have 1 pending job(s).") || !exists(s.home) {
		t.Errorf("declined uninstall: exit %d\n%s", code, out)
	}
	if out, _, code := s.runIn(s.work, "", "uninstall", "--purge"); code == 0 || !exists(s.home) {
		t.Errorf("uninstall without an answer: exit %d\n%s", code, out)
	}
	// Without --purge the data stays.
	out, _, code = s.runIn(s.work, "y\n", "uninstall")
	if code != 0 || !exists(filepath.Join(s.home, "laterr.db")) || !strings.Contains(out, "Kept job history and logs") {
		t.Errorf("uninstall: exit %d\n%s", code, out)
	}
	if j := s.job(1); j.Status != "pending" {
		t.Errorf("job 1 after uninstall: %s", j.Status)
	}
	out, _, code = s.runIn(s.work, "yes\n", "uninstall", "--purge")
	if code != 0 || exists(s.home) {
		t.Errorf("uninstall --purge: exit %d\n%s", code, out)
	}
	// Read-only commands do not bring the data directory back.
	s.ok("ls")
	if exists(s.home) {
		t.Error("ls recreated the data directory")
	}
}

func TestUsageErrors(t *testing.T) {
	s := newSandbox(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"+10x", "--", "ls"}, "Error: invalid time expression: +10x\n"},
		{[]string{"@2020-01-01/10:00", "--", "ls"}, "Error: scheduled time is in the past\n"},
		{[]string{"--grace", "10x", "+1m", "--", "ls"}, "Error: invalid duration for --grace: 10x\n"},
		{[]string{"show", "42"}, "Error: job #42 does not exist\n"},
		{[]string{"log", "42"}, "Error: job #42 does not exist\n"},
		{[]string{"+1m", "ls"}, "Error: unexpected argument: ls"},
		{[]string{"+1m"}, "Error: missing `--`"},
		{[]string{"+1m", "--"}, "Error: no command given"},
		{[]string{"--", "ls"}, "Error: missing time"},
		{[]string{"--bogus", "+1m", "--", "ls"}, "Error: unknown option: --bogus"},
		{[]string{"frobnicate"}, "Error: unknown command: frobnicate"},
		{[]string{"ls", "--bogus"}, "Error: unknown option for ls: --bogus"},
		{[]string{"rm", "abc"}, "Error: invalid job ID: abc"},
		{[]string{"rm"}, "Error: usage: laterr rm <id>..."},
		{[]string{"kill", "42"}, "Error: job #42 does not exist\n"},
		{[]string{"10m", "--", "ls"}, "Error: unknown command: 10m (did you mean +10m?)"},
		{[]string{"13:00", "--", "ls"}, "Error: unknown command: 13:00 (did you mean @13:00?)"},
		{[]string{"prune", "--older-than", "10x"}, "Error: invalid duration for --older-than: 10x\n"},
		{[]string{"prune", "--all", "--older-than", "1d"}, "Error: --all and --older-than cannot be combined"},
		{[]string{"prune", "--bogus"}, "Error: unknown option for prune: --bogus"},
	}
	for _, c := range cases {
		out, errOut, code := s.run(c.args...)
		if code == 0 || out != "" || !strings.HasPrefix(errOut, c.want) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", c.args, code, out, errOut)
		}
	}
	if out := s.ok("--version"); !strings.HasPrefix(out, "laterr ") {
		t.Errorf("--version: %q", out)
	}
	contains(t, "--help", s.ok("--help"), "laterr [options] <time> -- <command> [args...]", "--grace", "--no-env")
	if exists(s.home) {
		t.Error("failed commands created the data directory")
	}
}

// The follower is what a terminal gets by default. It is exercised directly
// here; the CLI only spawns it when its output is a terminal.
func TestFollower(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()

	out := s.ok("+3s", "--", "sh", "-c", "echo out; echo err >&2; sleep 1; echo late; exit 3")
	// Output captured by the test is not a terminal, so nothing stays behind.
	contains(t, "schedule output", out, "Output     `laterr log 1`")
	start := time.Now()
	stdout, stderr, code := s.run("__follow", "1", "2")
	if code != 0 || stdout != "out\nerr\nlate\n" {
		t.Errorf("follower: exit %d, stdout %q", code, stdout)
	}
	if want := "laterr: job #1 (sh -c 'echo out; echo err >&2; sleep 1; echo late; exit 3') runs in 2 seconds (cancel: laterr rm 1)\nlaterr: job #1 failed (exit code 3)\n"; stderr != want {
		t.Errorf("follower stderr:\n got %q\nwant %q", stderr, want)
	}
	if d := time.Since(start); d < 3*time.Second {
		t.Errorf("follower returned after %v, before the job finished", d)
	}

	// No reminder when the job is due sooner than the notice. Success is
	// reported too: a quiet command would otherwise leave no trace here.
	succeeded := func(id int) *regexp.Regexp {
		return regexp.MustCompile(fmt.Sprintf(`^laterr: job #%d succeeded \(\d+\.\ds\)\n$`, id))
	}
	s.ok("+1s", "--", "echo", "quick")
	if stdout, stderr, _ := s.run("__follow", "2", "5"); stdout != "quick\n" || !succeeded(2).MatchString(stderr) {
		t.Errorf("follower without reminder: stdout %q, stderr %q", stdout, stderr)
	}
	// --no-remind is passed on as zero. The shell's process group is passed
	// as a third argument; with no terminal it makes no difference.
	s.ok("+2s", "--", "true")
	if stdout, stderr, _ := s.run("__follow", "3", "0", "12345"); stdout != "" || !succeeded(3).MatchString(stderr) {
		t.Errorf("follower with no reminder: stdout %q, stderr %q", stdout, stderr)
	}
	// A job that cannot start is reported; a cancelled one is simply dropped.
	s.ok("+1s", "--", "laterr-no-such-command")
	if _, stderr, _ := s.run("__follow", "4", "0"); !strings.Contains(stderr, "laterr: job #4 failed: command not found: laterr-no-such-command") {
		t.Errorf("follower of an unstartable job: %q", stderr)
	}
	s.ok("+1h", "--", "true")
	s.ok("rm", "5")
	if stdout, stderr, code := s.run("__follow", "5", "5"); code != 0 || stdout+stderr != "" {
		t.Errorf("follower of a cancelled job: exit %d, %q %q", code, stdout, stderr)
	}

	// --bg and the reminder options.
	contains(t, "--bg output", s.ok("--bg", "+1h", "--", "true"), "Output     `laterr log 6`")
	if _, errOut, code := s.run("--bg", "--remind", "5s", "+1h", "--", "true"); code == 0 || !strings.HasPrefix(errOut, "Error: --remind has no effect with --bg") {
		t.Errorf("--bg --remind: exit %d, %q", code, errOut)
	}
	if _, errOut, code := s.run("--remind", "10x", "+1h", "--", "true"); code == 0 || errOut != "Error: invalid duration for --remind: 10x\n" {
		t.Errorf("--remind 10x: exit %d, %q", code, errOut)
	}
}

// terminal is a terminal the CLI was run on.
type terminal struct {
	t   *testing.T
	tty *os.File

	mu     sync.Mutex
	screen bytes.Buffer
}

// onTerminal runs the CLI on a terminal of its own. A shell runs it and then
// stays, as an interactive one would: the terminal lasts only as long as its
// session leader.
func (s *sandbox) onTerminal(cols, rows uint16, args ...string) *terminal {
	s.t.Helper()
	// Like an interactive shell, it survives Ctrl-C.
	cmd := exec.Command("sh", append([]string{"-c", `trap : INT; "$@"; while :; do sleep 1; done`, "sh", binary}, args...)...)
	cmd.Dir = s.work
	cmd.Env = s.env()
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		s.t.Fatal(err)
	}
	term := &terminal{t: s.t, tty: tty}
	s.t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		tty.Close()
	})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := tty.Read(buf)
			term.mu.Lock()
			term.screen.Write(buf[:n])
			term.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return term
}

// waitFor returns everything that appeared on the terminal by the time want
// did.
func (term *terminal) waitFor(want string) string {
	term.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		term.mu.Lock()
		got := term.screen.String()
		term.mu.Unlock()
		if strings.Contains(got, want) {
			return got
		}
		if time.Now().After(deadline) {
			term.t.Fatalf("%q did not appear on the terminal:\n%s", want, got)
		}
	}
}

// A job whose output is shown in a terminal runs on a terminal itself, of the
// same size, so that it draws what it would when run by hand.
func TestJobTerminal(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()

	// stdin is still /dev/null. The carriage return is what a progress bar
	// redraws its line with.
	script := `test -t 1 && test -t 2 && ! test -t 0 && stty size <&1; printf '10%%\r100%%\n'`
	screen := s.onTerminal(97, 31, "--no-remind", "+1s", "--", "sh", "-c", script).waitFor("job #1 succeeded")
	contains(t, "terminal", screen, "shown here while this terminal is open", "31 97\r\n10%\r100%\r\n")
	// The log holds what the job wrote, without the terminal's line endings.
	if got := s.readLog(s.job(1)); got != "31 97\n10%\r100%\n" {
		t.Errorf("log: %q", got)
	}

	// Nothing is lost between a job that exits at once and its log.
	s.onTerminal(80, 24, "--no-remind", "+1s", "--", "seq", "1", "50000").waitFor("job #2 succeeded")
	var want strings.Builder
	for i := 1; i <= 50000; i++ {
		fmt.Fprintln(&want, i)
	}
	if got := s.readLog(s.job(2)); got != want.String() {
		t.Errorf("log: %d bytes, want %d", len(got), want.Len())
	}

	// Something the job leaves running does not hold it up.
	s.onTerminal(80, 24, "--no-remind", "+1s", "--", "sh", "-c", "sleep 30 & echo started").waitFor("job #3 succeeded")
	if j := s.job(3); j.FinishedAt.Sub(*j.StartedAt) > 5*time.Second {
		t.Errorf("job waited for what it left behind: %v", j.FinishedAt.Sub(*j.StartedAt))
	}

	// The job's terminal follows the size of the one it is shown in, and the
	// job is told, as it would be when run by hand.
	script = `trap 'stty size <&1; exit' WINCH; echo ready; while :; do sleep 0.1; done`
	term := s.onTerminal(80, 24, "--no-remind", "+1s", "--", "sh", "-c", script)
	term.waitFor("ready")
	if err := pty.Setsize(term.tty, &pty.Winsize{Cols: 132, Rows: 43}); err != nil {
		t.Fatal(err)
	}
	term.waitFor("job #4 succeeded")
	if got := s.readLog(s.job(4)); got != "ready\n43 132\n" {
		t.Errorf("log: %q", got)
	}

	// Without a terminal to show it in, output goes straight to the log.
	s.ok("+1s", "--", "sh", "-c", "test -t 1 || echo plain")
	if got := s.readLog(s.wait(5, "succeeded")); got != "plain\n" {
		t.Errorf("log: %q", got)
	}
}

// Ctrl-C at the prompt of the terminal showing a job stops the job, but only
// when pressed twice: once is how a half-typed line is thrown away.
func TestInterrupt(t *testing.T) {
	s := newSandbox(t)
	s.startDaemon()
	ctrlC := func(term *terminal) {
		t.Helper()
		if _, err := term.tty.Write([]byte{3}); err != nil {
			t.Fatal(err)
		}
	}

	term := s.onTerminal(80, 24, "--remind", "1s", "+3s", "--", "sh", "-c", "echo started; sleep 60")
	// Before the job runs, Ctrl-C is none of the follower's business.
	// The reminder confirms that the follower has installed its signal handler,
	// including when startup is slowed down by race instrumentation.
	term.waitFor("runs in 1 second (cancel: laterr rm 1)")
	ctrlC(term)
	term.waitFor("started\r\n")
	ctrlC(term)
	term.waitFor("job #1 (sh -c 'echo started; sleep 60') is running; press Ctrl-C again to stop it")
	if j := s.job(1); j.Status != "running" {
		t.Fatalf("job 1 after one Ctrl-C: %s", j.Status)
	}
	ctrlC(term)
	term.waitFor("job #1 failed: stopped with Ctrl-C")
	if j := s.job(1); j.Status != "failed" || j.ErrorMessage == nil || *j.ErrorMessage != "stopped with Ctrl-C" {
		t.Errorf("job 1 after two Ctrl-C: %+v", j)
	}

	// A second press that comes too late is a first one again.
	term = s.onTerminal(80, 24, "--no-remind", "+1s", "--", "sh", "-c", "echo started; sleep 5")
	term.waitFor("started\r\n")
	ctrlC(term)
	term.waitFor("press Ctrl-C again")
	time.Sleep(3500 * time.Millisecond)
	ctrlC(term)
	term.waitFor("job #2 succeeded")
}
