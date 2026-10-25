// Package store persists jobs in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/khirayama/laterr/internal/paths"

	_ "modernc.org/sqlite"
)

// Status is where a job is in its life. Pending and Running are the only
// statuses a job can leave; the others are final.
type Status string

const (
	Pending     Status = "pending"
	Running     Status = "running"
	Succeeded   Status = "succeeded"
	Failed      Status = "failed"
	Interrupted Status = "interrupted"
	Cancelled   Status = "cancelled"
	Missed      Status = "missed"
)

// Job is everything about a job except its environment. The environment may
// hold secrets, so it is only ever read through Env, by the scheduler.
type Job struct {
	ID           int64
	Argv         []string
	Cwd          string
	CreatedAt    time.Time
	ScheduledAt  time.Time
	Grace        time.Duration
	StartedAt    time.Time // zero if never started
	FinishedAt   time.Time // zero if not finished
	Status       Status
	PID          int  // 0 if unknown
	ExitCode     *int // nil if the process did not exit normally
	ErrorMessage string
	LogPath      string
	// TTYCols and TTYRows are the size of the terminal the job was scheduled
	// from, if its output is shown there. The job then runs on a terminal of
	// that size instead of writing straight to its log. Zero otherwise.
	TTYCols, TTYRows int
}

// Deadline is the last moment at which the job may still be started.
func (j Job) Deadline() time.Time {
	return j.ScheduledAt.Add(j.Grace)
}

// NewJob is what Create needs to know about a job.
type NewJob struct {
	Argv        []string
	Cwd         string
	Env         []string
	CreatedAt   time.Time
	ScheduledAt time.Time
	Grace       time.Duration
	TTYCols     int
	TTYRows     int
}

// ErrNotFound is returned when no job has the given ID.
var ErrNotFound = errors.New("job does not exist")

// StateError reports that a transition was refused because of the job's
// current status.
type StateError struct {
	ID     int64
	Status Status
}

func (e *StateError) Error() string {
	return fmt.Sprintf("job #%d is %s", e.ID, e.Status)
}

// Store is a handle on the job database. Several processes may have the same
// database open at once.
type Store struct {
	db *sql.DB
}

// schemaVersion is stored in the database as PRAGMA user_version. Raise it
// and add a step to migrate whenever the schema changes.
const schemaVersion = 2

// ErrNewerSchema is returned when the database was written by a later version
// of laterr than this one.
var ErrNewerSchema = errors.New("the database was created by a newer version of laterr; upgrade this binary")

// noEnv replaces a job's environment once it can no longer run.
const noEnv = `[]`

// Timestamps are stored as UTC milliseconds since the Unix epoch.
const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	argv          TEXT    NOT NULL,
	cwd           TEXT    NOT NULL,
	env           TEXT    NOT NULL,
	created_at    INTEGER NOT NULL,
	scheduled_at  INTEGER NOT NULL,
	grace_seconds INTEGER NOT NULL,
	started_at    INTEGER,
	finished_at   INTEGER,
	status        TEXT    NOT NULL,
	pid           INTEGER,
	exit_code     INTEGER,
	error_message TEXT    NOT NULL DEFAULT '',
	log_path      TEXT    NOT NULL DEFAULT '',
	tty_cols      INTEGER NOT NULL DEFAULT 0,
	tty_rows      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS jobs_status_scheduled_at ON jobs (status, scheduled_at);
`

// Open opens the database at path, creating it (mode 0600) if necessary.
func Open(path string) (*Store, error) {
	f, err := paths.OpenPrivateFile(path, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	// BEGIN IMMEDIATE takes the write lock up front, so concurrent writers
	// queue on busy_timeout instead of failing halfway through a transaction.
	// secure_delete overwrites freed content, so that an environment cleared
	// from a finished job does not linger in the file.
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=secure_delete(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// migrate brings the schema up to schemaVersion. The common case, an
// up-to-date database, is a single read.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	if version > schemaVersion {
		return ErrNewerSchema
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Another process may have migrated while we waited for the write lock.
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return ErrNewerSchema
	}
	if version < 1 {
		// Version 0 is either a new file or a database from before versions
		// were recorded, which kept the environment of finished jobs.
		if _, err := tx.Exec(schema); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE jobs SET env = ? WHERE status NOT IN (?, ?)`, noEnv, Pending, Running); err != nil {
			return err
		}
	} else if version < 2 {
		// Version 2 added the terminal size. A database created above already
		// has it.
		for _, col := range []string{"tty_cols", "tty_rows"} {
			if _, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN ` + col + ` INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close releases the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// Create inserts a pending job. logPath derives the log location from the
// newly assigned ID.
func (s *Store) Create(n NewJob, logPath func(id int64) string) (Job, error) {
	argv, err := json.Marshal(n.Argv)
	if err != nil {
		return Job{}, err
	}
	env, err := json.Marshal(n.Env)
	if err != nil {
		return Job{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO jobs (argv, cwd, env, created_at, scheduled_at, grace_seconds, status, tty_cols, tty_rows)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(argv), n.Cwd, string(env), ms(n.CreatedAt), ms(n.ScheduledAt), int64(n.Grace/time.Second), Pending,
		n.TTYCols, n.TTYRows)
	if err != nil {
		return Job{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Job{}, err
	}
	if _, err := tx.Exec(`UPDATE jobs SET log_path = ? WHERE id = ?`, logPath(id), id); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.Get(id)
}

const jobColumns = `id, argv, cwd, created_at, scheduled_at, grace_seconds, started_at, finished_at,
	status, pid, exit_code, error_message, log_path, tty_cols, tty_rows`

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(row scanner) (Job, error) {
	var (
		j                         Job
		argv                      string
		created, scheduled, grace int64
		started, finished         sql.NullInt64
		pid, exitCode             sql.NullInt64
	)
	err := row.Scan(&j.ID, &argv, &j.Cwd, &created, &scheduled, &grace, &started, &finished,
		&j.Status, &pid, &exitCode, &j.ErrorMessage, &j.LogPath, &j.TTYCols, &j.TTYRows)
	if err != nil {
		return Job{}, err
	}
	if err := json.Unmarshal([]byte(argv), &j.Argv); err != nil {
		return Job{}, fmt.Errorf("job #%d: corrupt command: %w", j.ID, err)
	}
	j.CreatedAt = time.UnixMilli(created)
	j.ScheduledAt = time.UnixMilli(scheduled)
	j.Grace = time.Duration(grace) * time.Second
	if started.Valid {
		j.StartedAt = time.UnixMilli(started.Int64)
	}
	if finished.Valid {
		j.FinishedAt = time.UnixMilli(finished.Int64)
	}
	if pid.Valid {
		j.PID = int(pid.Int64)
	}
	if exitCode.Valid {
		c := int(exitCode.Int64)
		j.ExitCode = &c
	}
	return j, nil
}

// Get returns the job with the given ID, or ErrNotFound.
func (s *Store) Get(id int64) (Job, error) {
	j, err := scanJob(s.db.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return j, err
}

// querier is the part of *sql.DB and *sql.Tx that reading jobs needs.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// queryJobs runs a query that selects jobColumns.
func queryJobs(q querier, query string, args ...any) ([]Job, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// List returns jobs ordered by ID: only pending and running ones unless all
// is set.
func (s *Store) List(all bool) ([]Job, error) {
	if all {
		return queryJobs(s.db, `SELECT `+jobColumns+` FROM jobs ORDER BY id`)
	}
	return queryJobs(s.db, `SELECT `+jobColumns+` FROM jobs WHERE status IN (?, ?) ORDER BY id`, Pending, Running)
}

// Due returns pending jobs scheduled at or before now, oldest first.
func (s *Store) Due(now time.Time) ([]Job, error) {
	return queryJobs(s.db, `SELECT `+jobColumns+` FROM jobs WHERE status = ? AND scheduled_at <= ?
		ORDER BY scheduled_at, id`, Pending, ms(now))
}

// NextScheduled returns the earliest scheduled time among pending jobs.
func (s *Store) NextScheduled() (t time.Time, ok bool, err error) {
	var v sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(scheduled_at) FROM jobs WHERE status = ?`, Pending).Scan(&v); err != nil {
		return time.Time{}, false, err
	}
	if !v.Valid {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(v.Int64), true, nil
}

// Env returns the environment captured for a job, as "KEY=value" entries.
func (s *Store) Env(id int64) ([]string, error) {
	var raw string
	err := s.db.QueryRow(`SELECT env FROM jobs WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var env []string
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, fmt.Errorf("job #%d: corrupt environment: %w", id, err)
	}
	return env, nil
}

// transition runs a single conditional UPDATE, which SQLite applies
// atomically: of any number of competing transitions out of a status, exactly
// one sees a row to change. The others get a StateError (or ErrNotFound).
func (s *Store) transition(id int64, from Status, set string, args ...any) error {
	args = append(args, id, from)
	res, err := s.db.Exec(`UPDATE jobs SET `+set+` WHERE id = ? AND status = ?`, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	j, err := s.Get(id)
	if err != nil {
		return err
	}
	return &StateError{ID: id, Status: j.Status}
}

// Every transition to a final status also drops the captured environment: it
// may hold secrets, and a job that cannot run any more has no use for it.

// Cancel moves a pending job to cancelled.
func (s *Store) Cancel(id int64, now time.Time) error {
	return s.transition(id, Pending, `status = ?, finished_at = ?, env = ?`, Cancelled, ms(now), noEnv)
}

// RunNow makes a pending job due at now instead of at its scheduled time. It
// stays pending: the scheduler starts it, as it does any job that is due.
func (s *Store) RunNow(id int64, now time.Time) error {
	return s.transition(id, Pending, `scheduled_at = ?`, ms(now))
}

// MarkMissed moves a pending job to missed.
func (s *Store) MarkMissed(id int64, now time.Time) error {
	return s.transition(id, Pending, `status = ?, finished_at = ?, error_message = ?, env = ?`,
		Missed, ms(now), "not run: the scheduler was not running until after the grace period", noEnv)
}

// MarkRunning moves a pending job to running. It must be called, and must
// succeed, before the job's process is started.
func (s *Store) MarkRunning(id int64, now time.Time) error {
	return s.transition(id, Pending, `status = ?, started_at = ?`, Running, ms(now))
}

// SetPID records the process ID of a running job.
func (s *Store) SetPID(id int64, pid int) error {
	return s.transition(id, Running, `pid = ?`, pid)
}

// Finish moves a running job to a final status.
func (s *Store) Finish(id int64, status Status, exitCode *int, errMsg string, now time.Time) error {
	var code any
	if exitCode != nil {
		code = *exitCode
	}
	return s.transition(id, Running, `status = ?, exit_code = ?, error_message = ?, finished_at = ?, env = ?`,
		status, code, errMsg, ms(now), noEnv)
}

// InterruptRunning marks every running job as interrupted. The scheduler calls
// it at startup, when any job still recorded as running belongs to a previous
// scheduler process that is gone.
func (s *Store) InterruptRunning(now time.Time, errMsg string) (int64, error) {
	res, err := s.db.Exec(`UPDATE jobs SET status = ?, finished_at = ?, error_message = ?, env = ? WHERE status = ?`,
		Interrupted, ms(now), errMsg, noEnv, Running)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Prune deletes jobs that reached a final status before the given time and
// returns them, so that the caller can remove their logs. Pending and running
// jobs are never deleted. IDs are not reused afterwards.
func (s *Store) Prune(before time.Time) ([]Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	jobs, err := queryJobs(tx, `SELECT `+jobColumns+` FROM jobs
		WHERE status NOT IN (?, ?) AND finished_at IS NOT NULL AND finished_at < ? ORDER BY id`,
		Pending, Running, ms(before))
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if _, err := tx.Exec(`DELETE FROM jobs WHERE id = ?`, j.ID); err != nil {
			return nil, err
		}
	}
	return jobs, tx.Commit()
}

// CountActive returns the number of pending and running jobs.
func (s *Store) CountActive() (pending, running int, err error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM jobs WHERE status IN (?, ?) GROUP BY status`, Pending, Running)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var st Status
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return 0, 0, err
		}
		if st == Pending {
			pending = n
		} else {
			running = n
		}
	}
	return pending, running, rows.Err()
}
