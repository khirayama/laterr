// Package scheduler implements the background process that starts jobs when
// they are due, and the client the CLI uses to reach it.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/khirayama/laterr/internal/paths"
	"github.com/khirayama/laterr/internal/store"
)

// maxSleep bounds how long the scheduler waits between looks at the wall
// clock. Go timers run on the monotonic clock, which stops while the machine
// sleeps and ignores changes to the system time, so one long timer could fire
// arbitrarily late. Waking at least this often keeps that lateness bounded.
const maxSleep = 30 * time.Second

// stopGrace is how long running jobs get to exit after SIGTERM when the
// scheduler shuts down, before they are killed.
const stopGrace = 5 * time.Second

// Why a job that did not simply exit ended the way it did, as recorded with it.
const (
	interruptedMsg = "the scheduler stopped while this job was running"
	killedMsg      = "stopped with `laterr kill`"
	interruptMsg   = "stopped with Ctrl-C"
)

type scheduler struct {
	store   *store.Store
	version string
	log     *log.Logger
	wake    chan struct{}

	mu       sync.Mutex
	running  map[int64]*runningJob
	stopping bool
	wg       sync.WaitGroup // one per running job

	menu menuBar
}

type runningJob struct {
	cmd    *exec.Cmd
	out    *output
	killed string // why, if it was stopped on request: killedMsg or interruptMsg
}

// signal sends sig to the job and everything it started. Each job leads its
// own process group (see prepare), so this also reaches the children of a
// `sh -c` or a build tool.
func (r *runningJob) signal(sig syscall.Signal) {
	if err := syscall.Kill(-r.cmd.Process.Pid, sig); err != nil {
		r.cmd.Process.Signal(sig)
	}
}

// Run runs the scheduler until ctx is cancelled. Only one scheduler can run
// per data directory.
func Run(ctx context.Context, p paths.Paths, version string) error {
	if err := p.EnsureDirs(); err != nil {
		return err
	}
	lock, err := os.OpenFile(p.Lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another scheduler is already running for " + p.Dir)
	}

	st, err := store.Open(p.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	s := &scheduler{
		menu:    menuBar{paths: p},
		store:   st,
		version: version,
		log:     log.New(os.Stderr, "", log.LstdFlags),
		wake:    make(chan struct{}, 1),
		running: map[int64]*runningJob{},
	}

	// We hold the lock, so nothing is supervising jobs still marked running:
	// the scheduler that started them is gone. Whether they finished is
	// unknown, and running them again could repeat their side effects.
	n, err := st.InterruptRunning(time.Now(), interruptedMsg)
	if err != nil {
		return err
	}
	if n > 0 {
		s.log.Printf("marked %d job(s) from a previous run as interrupted", n)
	}

	l, err := listen(p.Socket)
	if err != nil {
		return err
	}
	defer os.Remove(p.Socket)
	defer l.Close()
	go s.serve(l)

	s.log.Printf("scheduler %s started (pid %d)", version, os.Getpid())
	for {
		s.startDue()
		s.showMenuBar()

		// With nothing pending the timer channel stays nil and the select
		// blocks until a notification arrives.
		var timer <-chan time.Time
		next, ok, err := st.NextScheduled()
		if err != nil {
			s.log.Printf("reading the schedule failed: %v", err)
			timer = time.After(maxSleep)
		} else if ok {
			timer = time.After(min(max(time.Until(next), 0), maxSleep))
		}

		select {
		case <-ctx.Done():
			s.menu.stop()
			s.shutdown()
			s.log.Printf("scheduler stopped")
			return nil
		case <-s.wake:
		case <-timer:
		}
	}
}

func (s *scheduler) notify() {
	select {
	case s.wake <- struct{}{}:
	default: // a wake-up is already queued
	}
}

// startDue starts every pending job whose time has come, oldest first, without
// waiting for any of them to finish. Jobs past their grace period are marked
// missed instead.
func (s *scheduler) startDue() {
	now := time.Now()
	due, err := s.store.Due(now)
	if err != nil {
		s.log.Printf("reading due jobs failed: %v", err)
		return
	}
	for _, j := range due {
		if now.After(j.Deadline()) {
			if err := s.store.MarkMissed(j.ID, now); err == nil {
				s.log.Printf("job #%d missed (was due %s)", j.ID, j.ScheduledAt.Format(time.RFC3339))
			} else if !isStateError(err) {
				s.log.Printf("job #%d: %v", j.ID, err)
			}
			continue
		}
		// Persist "running" before starting the process. If we crash between
		// the two, the job ends up interrupted rather than being run twice.
		if err := s.store.MarkRunning(j.ID, time.Now()); err != nil {
			if !isStateError(err) { // a StateError means it was just cancelled
				s.log.Printf("job #%d: %v", j.ID, err)
			}
			continue
		}
		s.start(j)
	}
}

func isStateError(err error) bool {
	var se *store.StateError
	return errors.As(err, &se)
}

// start launches a job that has already been marked running.
func (s *scheduler) start(j store.Job) {
	fail := func(err error) {
		s.log.Printf("job #%d failed to start: %v", j.ID, err)
		if err := s.store.Finish(j.ID, store.Failed, nil, err.Error(), time.Now()); err != nil {
			s.log.Printf("job #%d: %v", j.ID, err)
		}
	}
	env, err := s.store.Env(j.ID)
	if err != nil {
		fail(err)
		return
	}
	cmd, out, err := prepare(j, env)
	if err != nil {
		fail(err)
		return
	}

	r := &runningJob{cmd: cmd, out: out}
	s.mu.Lock()
	err = cmd.Start()
	if err == nil {
		s.running[j.ID] = r
		s.wg.Add(1)
	}
	s.mu.Unlock()
	if err != nil {
		out.close()
		fail(fmt.Errorf("cannot start %s: %w", j.Argv[0], unwrapPathError(err)))
		return
	}
	out.started()
	if err := s.store.SetPID(j.ID, cmd.Process.Pid); err != nil {
		s.log.Printf("job #%d: %v", j.ID, err)
	}
	s.log.Printf("job #%d started (pid %d)", j.ID, cmd.Process.Pid)

	go func() {
		defer s.wg.Done()
		err := cmd.Wait()
		out.close()

		s.mu.Lock()
		delete(s.running, j.ID)
		stopping, killed := s.stopping, r.killed
		s.mu.Unlock()

		status, code, msg := outcome(cmd.ProcessState, err)
		switch {
		case status == store.Succeeded:
		case stopping:
			status, code, msg = store.Interrupted, nil, interruptedMsg
		case killed != "":
			msg = killed
		}
		s.finish(j.ID, status, code, msg)
	}()
}

// kill stops a running job on request: SIGTERM first, SIGKILL if it is still
// there after stopGrace. The job is recorded as failed, with msg as the
// reason, when it exits.
func (s *scheduler) kill(id int64, msg string) error {
	s.mu.Lock()
	r, ok := s.running[id]
	if ok {
		r.killed = msg
		r.signal(syscall.SIGTERM)
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("job #%d is not running", id)
	}
	s.log.Printf("job #%d: stop requested", id)
	time.AfterFunc(stopGrace, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.running[id] == r {
			r.signal(syscall.SIGKILL)
		}
	})
	return nil
}

// resize gives a running job's terminal a new size and tells the job, as the
// kernel would if that were its controlling terminal.
func (s *scheduler) resize(id int64, cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.running[id]
	if !ok {
		return fmt.Errorf("job #%d is not running", id)
	}
	if r.out.resize(cols, rows) {
		r.signal(syscall.SIGWINCH)
	}
	return nil
}

func (s *scheduler) finish(id int64, status store.Status, code *int, msg string) {
	if err := s.store.Finish(id, status, code, msg, time.Now()); err != nil {
		s.log.Printf("job #%d: recording the result failed: %v", id, err)
		return
	}
	s.log.Printf("job #%d %s", id, status)
}

// shutdown stops running jobs: they do not outlive the scheduler.
func (s *scheduler) shutdown() {
	s.mu.Lock()
	s.stopping = true
	n := len(s.running)
	for _, r := range s.running {
		r.signal(syscall.SIGTERM)
	}
	s.mu.Unlock()
	if n == 0 {
		return
	}
	s.log.Printf("stopping %d running job(s)", n)

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(stopGrace):
	}
	s.mu.Lock()
	for _, r := range s.running {
		r.signal(syscall.SIGKILL)
	}
	s.mu.Unlock()
	select {
	case <-done:
	case <-time.After(stopGrace):
		// Still marked running; the next scheduler start records them as
		// interrupted.
	}
}
