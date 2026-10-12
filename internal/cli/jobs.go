package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/khirayama/laterr/internal/paths"
	"github.com/khirayama/laterr/internal/scheduler"
	"github.com/khirayama/laterr/internal/store"
)

// jobJSON is the machine-readable form of a job. It has no environment field:
// captured variables may hold secrets and are never printed.
type jobJSON struct {
	ID           int64    `json:"id"`
	Status       string   `json:"status"`
	Command      []string `json:"command"`
	Cwd          string   `json:"cwd"`
	CreatedAt    string   `json:"created_at"`
	ScheduledAt  string   `json:"scheduled_at"`
	GraceSeconds int64    `json:"grace_seconds"`
	StartedAt    *string  `json:"started_at"`
	FinishedAt   *string  `json:"finished_at"`
	PID          *int     `json:"pid"`
	ExitCode     *int     `json:"exit_code"`
	ErrorMessage *string  `json:"error_message"`
	LogPath      string   `json:"log_path"`
}

func toJSON(j store.Job) jobJSON {
	utc := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	out := jobJSON{
		ID: j.ID, Status: string(j.Status), Command: j.Argv, Cwd: j.Cwd,
		CreatedAt: utc(j.CreatedAt), ScheduledAt: utc(j.ScheduledAt),
		GraceSeconds: int64(j.Grace / time.Second),
		ExitCode:     j.ExitCode, LogPath: j.LogPath,
	}
	if !j.StartedAt.IsZero() {
		s := utc(j.StartedAt)
		out.StartedAt = &s
	}
	if !j.FinishedAt.IsZero() {
		s := utc(j.FinishedAt)
		out.FinishedAt = &s
	}
	if j.Status == store.Running && j.PID != 0 {
		out.PID = &j.PID
	}
	if j.ErrorMessage != "" {
		out.ErrorMessage = &j.ErrorMessage
	}
	return out
}

func (a *app) list(args []string) error {
	flags, positional, err := parseArgs("ls", args, "--all", "--json")
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return usagef("unexpected argument for ls: %s", positional[0])
	}
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	var jobs []store.Job
	st, err := openStore(p, false)
	if err != nil {
		return err
	}
	if st != nil {
		defer st.Close()
		if jobs, err = st.List(flags["--all"]); err != nil {
			return err
		}
	}

	if flags["--json"] {
		out := make([]jobJSON, len(jobs)) // encodes as [] rather than null when empty
		for i, j := range jobs {
			out[i] = toJSON(j)
		}
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	if len(jobs) == 0 {
		if flags["--all"] {
			fmt.Fprintln(a.stdout, "No jobs.")
		} else {
			fmt.Fprintln(a.stdout, "No pending or running jobs. Use `laterr ls --all` to see finished ones.")
		}
		return nil
	}
	now := time.Now()
	w := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tWHEN\tSTATUS\tCOMMAND")
	for _, j := range jobs {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", j.ID, when(j.ScheduledAt, now, " ", false), j.Status, shortCommand(j.Argv))
	}
	return w.Flush()
}

func (a *app) show(args []string) error {
	_, positional, err := parseArgs("show", args)
	if err != nil {
		return err
	}
	id, err := jobID("show", positional)
	if err != nil {
		return err
	}
	_, st, j, err := getJob(id)
	if err != nil {
		return err
	}
	defer st.Close()

	now := time.Now()
	row := func(label, value string) { fmt.Fprintf(a.stdout, "  %-10s %s\n", label, value) }
	fmt.Fprintf(a.stdout, "Job #%d\n", j.ID)
	row("Status", string(j.Status))
	row("Command", commandLine(j.Argv))
	row("Directory", tildify(j.Cwd))
	row("Created", when(j.CreatedAt, now, ", ", true))
	row("Scheduled", when(j.ScheduledAt, now, ", ", false))
	row("Grace", fmt.Sprintf("%s (not started after %s)", compactDuration(j.Grace), when(j.Deadline(), now, ", ", false)))
	if j.Status == store.Running && j.PID != 0 {
		row("PID", fmt.Sprint(j.PID))
	}
	if !j.StartedAt.IsZero() {
		row("Started", when(j.StartedAt, now, ", ", true))
	}
	if !j.FinishedAt.IsZero() {
		// For jobs that never ran this is when they were cancelled or missed.
		label := "Finished"
		switch j.Status {
		case store.Cancelled:
			label = "Cancelled"
		case store.Missed:
			label = "Missed"
		}
		row(label, when(j.FinishedAt, now, ", ", true))
	}
	if !j.StartedAt.IsZero() && !j.FinishedAt.IsZero() {
		row("Duration", elapsed(j.FinishedAt.Sub(j.StartedAt)))
	}
	if j.ExitCode != nil {
		row("Exit code", fmt.Sprint(*j.ExitCode))
	}
	if j.ErrorMessage != "" {
		row("Error", j.ErrorMessage)
	}
	row("Log", j.LogPath)
	return nil
}

// startWait is how long `laterr run` waits for the scheduler to start the job.
const startWait = 5 * time.Second

func (a *app) runNow(args []string) error {
	_, positional, err := parseArgs("run", args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return usagef("usage: laterr run <id>...")
	}
	// Check every ID before touching anything, so that a typo starts nothing.
	ids := make([]int64, len(positional))
	for i, arg := range positional {
		if ids[i], err = parseID(arg); err != nil {
			return err
		}
	}
	failed := false
	for _, id := range ids {
		if err := a.start(id); err != nil {
			failed = true
			fmt.Fprintf(a.stderr, "Error: %v\n", err)
			continue
		}
		fmt.Fprintf(a.stdout, "✓ Started job #%d\n", id)
	}
	if failed {
		return errSilent
	}
	return nil
}

func (a *app) start(id int64) error {
	p, st, _, err := getJob(id)
	if err != nil {
		return err
	}
	defer st.Close()
	// The scheduler starts the job, as it would have later. Without one the
	// job would be due and stay that way, so look for it first.
	if _, _, err := ensureScheduler(p); err != nil {
		return err
	}
	// Only pending jobs can be started, and the update is conditional on the
	// status: a job that was just cancelled stays cancelled, and one the
	// scheduler has just started does not run twice.
	if err := st.RunNow(id, time.Now()); err != nil {
		var se *store.StateError
		if errors.As(err, &se) {
			if se.Status == store.Running {
				return fmt.Errorf("job #%d is already running", id)
			}
			return fmt.Errorf("job #%d cannot be run (it %s)", id, pastTense(se.Status))
		}
		return err
	}
	if err := scheduler.Wake(p.Socket); err != nil {
		return err
	}
	var j store.Job
	started := waitFor(startWait, func() bool {
		j, err = st.Get(id)
		return err != nil || j.Status != store.Pending
	})
	if err != nil {
		return err
	}
	if !started {
		return fmt.Errorf("job #%d is due now but has not started (see `laterr doctor`)", id)
	}
	return nil
}

func (a *app) remove(args []string) error {
	_, positional, err := parseArgs("rm", args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return usagef("usage: laterr rm <id>...")
	}
	// Check every ID before touching anything, so that a typo cancels nothing.
	ids := make([]int64, len(positional))
	for i, arg := range positional {
		if ids[i], err = parseID(arg); err != nil {
			return err
		}
	}
	failed := false
	for _, id := range ids {
		if err := a.cancel(id); err != nil {
			failed = true
			fmt.Fprintf(a.stderr, "Error: %v\n", err)
			continue
		}
		fmt.Fprintf(a.stdout, "✓ Cancelled job #%d\n", id)
	}
	if failed {
		return errSilent
	}
	return nil
}

func (a *app) cancel(id int64) error {
	p, st, _, err := getJob(id)
	if err != nil {
		return err
	}
	defer st.Close()
	// Only pending jobs can be cancelled. The update is conditional on the
	// status, so a job the scheduler has just started cannot be cancelled, and
	// a cancelled job can never be started.
	if err := st.Cancel(id, time.Now()); err != nil {
		var se *store.StateError
		if errors.As(err, &se) {
			if se.Status == store.Running {
				return fmt.Errorf("job #%d is already running (stop it with `laterr kill %d`)", id, id)
			}
			return fmt.Errorf("job #%d is already %s", id, se.Status)
		}
		return err
	}
	scheduler.Wake(p.Socket) // best effort: lets the scheduler recompute its sleep
	return nil
}

// killWait is how long `laterr kill` waits for the job to go away. The
// scheduler gives it 5 seconds before killing it outright.
const killWait = 8 * time.Second

func (a *app) kill(args []string) error {
	_, positional, err := parseArgs("kill", args)
	if err != nil {
		return err
	}
	id, err := jobID("kill", positional)
	if err != nil {
		return err
	}
	p, st, j, err := getJob(id)
	if err != nil {
		return err
	}
	defer st.Close()
	if j.Status == store.Pending {
		return fmt.Errorf("job #%d has not started yet (cancel it with `laterr rm %d`)", id, id)
	}
	if j.Status != store.Running {
		return fmt.Errorf("job #%d is not running (it %s)", id, pastTense(j.Status))
	}
	// The scheduler owns the process, so it does the stopping: the job and
	// everything it started.
	if err := scheduler.Kill(p.Socket, id); err != nil {
		return err
	}
	stopped := waitFor(killWait, func() bool {
		j, err = st.Get(id)
		return err != nil || j.Status != store.Running
	})
	if err != nil {
		return err
	}
	if !stopped {
		return fmt.Errorf("job #%d was told to stop but is still running", id)
	}
	fmt.Fprintf(a.stdout, "✓ Stopped job #%d\n", id)
	return nil
}

// pastTense completes "job #1 ..." for a status.
func pastTense(s store.Status) string {
	switch s {
	case store.Succeeded, store.Failed:
		return "has " + string(s)
	}
	return "was " + string(s)
}
