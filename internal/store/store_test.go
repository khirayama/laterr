package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func logPath(id int64) string { return fmt.Sprintf("/logs/%d.log", id) }

func open(t *testing.T) (*Store, string) {
	t.Helper()
	// A space in the path, as in "~/Library/Application Support".
	path := filepath.Join(t.TempDir(), "data dir", "laterr.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

var t0 = time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)

func add(t *testing.T, s *Store, at time.Time, grace time.Duration) Job {
	t.Helper()
	j, err := s.Create(NewJob{
		Argv: []string{"echo", "hi"}, Cwd: "/tmp", Env: []string{"A=1"},
		CreatedAt: t0, ScheduledAt: at, Grace: grace,
	}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestCreateAndGet(t *testing.T) {
	s, path := open(t)
	argv := []string{"sh", "-c", "echo 'a b' \"c\"\n$HOME; *", "", "日本語 🎉"}
	env := []string{"PATH=/bin", "SECRET=hunter2", "MULTI=a\nb"}
	j, err := s.Create(NewJob{
		Argv: argv, Cwd: "/tmp/my app", Env: env,
		CreatedAt: t0, ScheduledAt: t0.Add(90 * time.Second), Grace: 24 * time.Hour,
	}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != 1 || j.Status != Pending || j.LogPath != "/logs/1.log" {
		t.Errorf("unexpected job: %+v", j)
	}
	if !reflect.DeepEqual(j.Argv, argv) || j.Cwd != "/tmp/my app" {
		t.Errorf("argv/cwd not preserved: %+v", j)
	}
	if !j.CreatedAt.Equal(t0) || !j.ScheduledAt.Equal(t0.Add(90*time.Second)) || j.Grace != 24*time.Hour {
		t.Errorf("times not preserved: %+v", j)
	}
	if !j.StartedAt.IsZero() || !j.FinishedAt.IsZero() || j.PID != 0 || j.ExitCode != nil {
		t.Errorf("unexpected run data: %+v", j)
	}
	if !j.Deadline().Equal(t0.Add(90*time.Second + 24*time.Hour)) {
		t.Errorf("deadline: %v", j.Deadline())
	}
	got, err := s.Env(j.ID)
	if err != nil || !reflect.DeepEqual(got, env) {
		t.Errorf("env: %v, %v", got, err)
	}
	if _, err := s.Get(99); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(99): %v", err)
	}
	if _, err := s.Env(99); !errors.Is(err, ErrNotFound) {
		t.Errorf("Env(99): %v", err)
	}
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err != nil {
			t.Errorf("stat %s: %v", p, err)
		} else if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, want 0600", p, fi.Mode().Perm())
		}
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	s, path := open(t)
	a := add(t, s, t0.Add(time.Hour), time.Hour)
	if err := s.Cancel(a.ID, t0); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	// IDs keep counting up and are never reused.
	b := add(t, s2, t0.Add(time.Hour), time.Hour)
	if b.ID != 2 {
		t.Errorf("got ID %d, want 2", b.ID)
	}
	if j, err := s2.Get(a.ID); err != nil || j.Status != Cancelled {
		t.Errorf("job 1 after reopen: %+v, %v", j, err)
	}
}

func TestTransitions(t *testing.T) {
	s, _ := open(t)
	wantState := func(err error, st Status) {
		t.Helper()
		var se *StateError
		if !errors.As(err, &se) || se.Status != st {
			t.Errorf("got %v, want StateError{%s}", err, st)
		}
	}
	now := t0.Add(time.Minute)

	// pending -> running -> succeeded
	a := add(t, s, t0, time.Hour)
	if err := s.Finish(a.ID, Succeeded, nil, "", now); err == nil {
		t.Error("finishing a pending job should fail")
	}
	if err := s.SetPID(a.ID, 1); err == nil {
		t.Error("setting the PID of a pending job should fail")
	}
	if err := s.MarkRunning(a.ID, now); err != nil {
		t.Fatal(err)
	}
	wantState(s.MarkRunning(a.ID, now), Running)
	wantState(s.Cancel(a.ID, now), Running)
	wantState(s.MarkMissed(a.ID, now), Running)
	if err := s.SetPID(a.ID, 4321); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.Get(a.ID); j.Status != Running || j.PID != 4321 || !j.StartedAt.Equal(now) {
		t.Errorf("running job: %+v", j)
	}
	zero := 0
	if err := s.Finish(a.ID, Succeeded, &zero, "", now.Add(8200*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	j, _ := s.Get(a.ID)
	if j.Status != Succeeded || j.ExitCode == nil || *j.ExitCode != 0 || j.FinishedAt.Sub(j.StartedAt) != 8200*time.Millisecond {
		t.Errorf("finished job: %+v", j)
	}
	// Final statuses are final.
	wantState(s.MarkRunning(a.ID, now), Succeeded)
	wantState(s.Cancel(a.ID, now), Succeeded)
	wantState(s.Finish(a.ID, Failed, nil, "", now), Succeeded)

	// pending -> running -> failed, with an exit code and a message
	b := add(t, s, t0, time.Hour)
	s.MarkRunning(b.ID, now)
	three := 3
	s.Finish(b.ID, Failed, &three, "boom", now)
	if j, _ := s.Get(b.ID); j.Status != Failed || *j.ExitCode != 3 || j.ErrorMessage != "boom" {
		t.Errorf("failed job: %+v", j)
	}

	// pending -> cancelled
	c := add(t, s, t0, time.Hour)
	if err := s.Cancel(c.ID, now); err != nil {
		t.Fatal(err)
	}
	wantState(s.Cancel(c.ID, now), Cancelled)
	wantState(s.MarkRunning(c.ID, now), Cancelled)
	if j, _ := s.Get(c.ID); j.Status != Cancelled || !j.StartedAt.IsZero() {
		t.Errorf("cancelled job: %+v", j)
	}

	// pending -> missed
	d := add(t, s, t0, time.Hour)
	if err := s.MarkMissed(d.ID, now); err != nil {
		t.Fatal(err)
	}
	wantState(s.MarkRunning(d.ID, now), Missed)
	if j, _ := s.Get(d.ID); j.Status != Missed || j.ErrorMessage == "" {
		t.Errorf("missed job: %+v", j)
	}

	if err := s.Cancel(99, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("Cancel(99): %v", err)
	}
}

// The captured environment may hold secrets. It is kept exactly as long as
// the job can still run.
func TestEnvDroppedWhenFinal(t *testing.T) {
	s, _ := open(t)
	env := func(id int64) []string {
		t.Helper()
		e, err := s.Env(id)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	finals := map[string]func(id int64){
		"succeeded":   func(id int64) { s.MarkRunning(id, t0); s.Finish(id, Succeeded, nil, "", t0) },
		"failed":      func(id int64) { s.MarkRunning(id, t0); s.Finish(id, Failed, nil, "boom", t0) },
		"cancelled":   func(id int64) { s.Cancel(id, t0) },
		"missed":      func(id int64) { s.MarkMissed(id, t0) },
		"interrupted": func(id int64) { s.MarkRunning(id, t0); s.InterruptRunning(t0, "stopped") },
	}
	for name, finish := range finals {
		j := add(t, s, t0, time.Hour)
		if got := env(j.ID); len(got) != 1 {
			t.Fatalf("%s: pending job has env %q", name, got)
		}
		finish(j.ID)
		if got, _ := s.Get(j.ID); string(got.Status) != name {
			t.Fatalf("%s: job is %s", name, got.Status)
		}
		if got := env(j.ID); len(got) != 0 {
			t.Errorf("%s: env kept after the job ended: %q", name, got)
		}
	}
	// A running job still has it.
	r := add(t, s, t0, time.Hour)
	s.MarkRunning(r.ID, t0)
	if got := env(r.ID); len(got) != 1 {
		t.Errorf("running job lost its env: %q", got)
	}
}

func TestPrune(t *testing.T) {
	s, _ := open(t)
	old := add(t, s, t0, time.Hour)
	s.MarkRunning(old.ID, t0)
	s.Finish(old.ID, Succeeded, nil, "", t0)
	oldCancelled := add(t, s, t0, time.Hour)
	s.Cancel(oldCancelled.ID, t0.Add(time.Minute))
	recent := add(t, s, t0, time.Hour)
	s.MarkRunning(recent.ID, t0)
	s.Finish(recent.ID, Failed, nil, "", t0.Add(48*time.Hour))
	pending := add(t, s, t0, time.Hour)
	running := add(t, s, t0, time.Hour)
	s.MarkRunning(running.ID, t0)

	got, err := s.Prune(t0.Add(24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != old.ID || got[1].ID != oldCancelled.ID || got[0].LogPath != logPath(old.ID) {
		t.Errorf("pruned: %+v", got)
	}
	left, _ := s.List(true)
	if len(left) != 3 || left[0].ID != recent.ID || left[1].ID != pending.ID || left[2].ID != running.ID {
		t.Errorf("left: %+v", left)
	}
	// Pending and running jobs survive any cutoff.
	if got, _ := s.Prune(t0.Add(1000 * time.Hour)); len(got) != 1 || got[0].ID != recent.ID {
		t.Errorf("second prune: %+v", got)
	}
	// IDs are not handed out again.
	if j := add(t, s, t0, time.Hour); j.ID != running.ID+1 {
		t.Errorf("new job after pruning got ID %d", j.ID)
	}
}

func TestMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "laterr.db")
	raw := func(stmts ...string) {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for _, stmt := range stmts {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	// A database from before versions were recorded: it kept the environment
	// of finished jobs.
	insert := `INSERT INTO jobs (argv, cwd, env, created_at, scheduled_at, grace_seconds, status)
		VALUES ('["true"]', '/', '["SECRET=1"]', 0, 0, 60, '%s')`
	raw(schema, fmt.Sprintf(insert, Succeeded), fmt.Sprintf(insert, Pending))

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if env, _ := s.Env(1); len(env) != 0 {
		t.Errorf("finished job kept its env through the migration: %q", env)
	}
	if env, _ := s.Env(2); !reflect.DeepEqual(env, []string{"SECRET=1"}) {
		t.Errorf("pending job: env %q", env)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Errorf("user_version = %d, %v", version, err)
	}
	s.Close()

	// A database from a later laterr is refused rather than misread.
	raw(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion+1))
	if _, err := Open(path); !errors.Is(err, ErrNewerSchema) {
		t.Errorf("opening a newer database: %v", err)
	}
}

func TestInterruptRunning(t *testing.T) {
	s, _ := open(t)
	p := add(t, s, t0, time.Hour)
	r := add(t, s, t0, time.Hour)
	done := add(t, s, t0, time.Hour)
	s.MarkRunning(r.ID, t0)
	s.MarkRunning(done.ID, t0)
	s.Finish(done.ID, Succeeded, nil, "", t0)

	n, err := s.InterruptRunning(t0.Add(time.Minute), "scheduler stopped")
	if err != nil || n != 1 {
		t.Fatalf("got %d, %v", n, err)
	}
	for id, want := range map[int64]Status{p.ID: Pending, r.ID: Interrupted, done.ID: Succeeded} {
		if j, _ := s.Get(id); j.Status != want {
			t.Errorf("job %d: %s, want %s", id, j.Status, want)
		}
	}
	// An interrupted job is never picked up again.
	if due, _ := s.Due(t0.Add(time.Hour)); len(due) != 1 || due[0].ID != p.ID {
		t.Errorf("due: %+v", due)
	}
}

func TestDueListAndCounts(t *testing.T) {
	s, _ := open(t)
	if _, ok, err := s.NextScheduled(); ok || err != nil {
		t.Errorf("empty store: ok=%v err=%v", ok, err)
	}
	late := add(t, s, t0.Add(30*time.Second), time.Hour)  // 1
	early := add(t, s, t0.Add(10*time.Second), time.Hour) // 2
	tie := add(t, s, t0.Add(10*time.Second), time.Hour)   // 3
	future := add(t, s, t0.Add(time.Hour), time.Hour)     // 4
	gone := add(t, s, t0.Add(5*time.Second), time.Hour)   // 5
	s.Cancel(gone.ID, t0)

	if next, ok, _ := s.NextScheduled(); !ok || !next.Equal(early.ScheduledAt) {
		t.Errorf("next: %v %v", next, ok)
	}
	if due, _ := s.Due(t0.Add(9 * time.Second)); len(due) != 0 {
		t.Errorf("nothing should be due yet: %+v", due)
	}
	// Oldest scheduled time first, ties broken by ID; cancelled jobs excluded.
	due, err := s.Due(t0.Add(30 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, j := range due {
		ids = append(ids, j.ID)
	}
	if want := []int64{early.ID, tie.ID, late.ID}; !reflect.DeepEqual(ids, want) {
		t.Errorf("due order: %v, want %v", ids, want)
	}

	s.MarkRunning(early.ID, t0)
	if p, r, _ := s.CountActive(); p != 3 || r != 1 {
		t.Errorf("counts: %d pending, %d running", p, r)
	}
	if l, _ := s.List(false); len(l) != 4 || l[0].ID != late.ID || l[3].ID != future.ID {
		t.Errorf("active list: %+v", l)
	}
	if l, _ := s.List(true); len(l) != 5 {
		t.Errorf("full list has %d jobs", len(l))
	}
}

// Several connections, as with several CLI processes, race to create jobs and
// to claim the same job.
func TestConcurrentAccess(t *testing.T) {
	_, path := open(t)
	const workers, perWorker = 8, 10
	stores := make([]*Store, workers)
	for i := range stores {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		stores[i] = s
	}

	var wg sync.WaitGroup
	ids := make(chan int64, workers*perWorker)
	for _, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				j, err := s.Create(NewJob{Argv: []string{"true"}, Cwd: "/", CreatedAt: t0, ScheduledAt: t0, Grace: time.Hour}, logPath)
				if err != nil {
					t.Error(err)
					return
				}
				if j.LogPath != logPath(j.ID) {
					t.Errorf("job %d has log path %s", j.ID, j.LogPath)
				}
				ids <- j.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[int64]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate ID %d", id)
		}
		seen[id] = true
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("created %d jobs, want %d", len(seen), workers*perWorker)
	}

	// For every job, one connection tries to run it while the others try to
	// cancel it or mark it missed. Exactly one transition may win.
	for id := range seen {
		var won int32
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i, s := range stores {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var err error
				switch i % 3 {
				case 0:
					err = s.MarkRunning(id, t0)
				case 1:
					err = s.Cancel(id, t0)
				default:
					err = s.MarkMissed(id, t0)
				}
				var se *StateError
				if err == nil {
					mu.Lock()
					won++
					mu.Unlock()
				} else if !errors.As(err, &se) {
					t.Errorf("job %d: %v", id, err)
				}
			}()
		}
		wg.Wait()
		if won != 1 {
			t.Errorf("job %d: %d transitions succeeded, want exactly 1", id, won)
		}
	}
}
