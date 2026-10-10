# laterr

[![CI](https://github.com/khirayama/laterr/actions/workflows/ci.yml/badge.svg)](https://github.com/khirayama/laterr/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/khirayama/laterr)](https://github.com/khirayama/laterr/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/khirayama/laterr)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**A tiny CLI to run commands later.**

English | [日本語](README.ja.md)

```console
$ laterr +10m -- npm install
✓ Scheduled job #42

  Command    npm install
  Run at     Today, 13:00 (in 10 minutes)
  Directory  ~/projects/my-app
  Env        47 variables captured
  If late    runs on wake or login, skipped after Tomorrow, 13:00
  Output     shown here while this terminal is open; always in `laterr log 42`

  Runs even after this terminal is closed. Stops if you log out mid-run.
$                                                  # keep working...
laterr: job #42 (npm install) runs in 5 seconds    # ...ten minutes later
added 312 packages in 8s
laterr: job #42 succeeded (8.2s)
```

`laterr` runs a command **once**, at a given time or after a given delay. It
is not cron: there is nothing recurring about it.

Think of it as a `sleep 600 && npm install` that gives your prompt back, and
does not depend on the terminal. Keep the terminal open and the output shows
up right there. Close it and the job still runs; the output and exit code are
waiting for you in `laterr log` and `laterr show`.

## Features

- **Short syntax**: `laterr +10m -- <command>`, `laterr @13:00 -- <command>`
- **Output where you are**: the terminal you scheduled from gets the output
  live, with a heads-up shortly before the command runs
- **Same context**: the command runs in the directory and with the environment
  you scheduled it from
- **Job management**: list, cancel, stop and inspect jobs; stdout, stderr and
  the exit code are kept
- **No root, no cron, no `at`**: a small per-user background service does the
  work, and installs itself on first use
- **macOS and Linux**, a single static binary with no runtime dependencies

## Table of contents

- [Why not `at`?](#why-not-at)
- [Install](#install)
- [Quick start](#quick-start)
- [Time syntax](#time-syntax)
- [Commands](#commands)
- [What happens when...](#what-happens-when)
- [Environment](#environment)
- [Platform notes](#platform-notes)
- [Troubleshooting](#troubleshooting)
- [How it works](#how-it-works)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)

## Why not `at`?

`at` does the same job on paper, but:

- On macOS, `atrun` is disabled by default and enabling it requires `sudo`.
  `laterr` needs no root and sets itself up on first use.
- `at` mails the output to a local mailbox that few machines have configured.
  `laterr` keeps a log per job (`laterr log <id>`), together with its status,
  start and end times and exit code (`laterr show <id>`, `laterr ls --all`).
- `at` reads a shell script from stdin. `laterr` takes the command as
  arguments and runs it directly, so quoting works the way you typed it.

## Install

Download the archive for your platform (macOS or Linux, arm64 or amd64) from
the [releases page](https://github.com/khirayama/laterr/releases), then put
the binary somewhere on your `PATH`:

```sh
tar xzf laterr_*_darwin_arm64.tar.gz
install -m 755 laterr ~/.local/bin/laterr      # or /usr/local/bin
```

Or build it with Go 1.26 or later:

```sh
go install github.com/khirayama/laterr/cmd/laterr@latest
```

There is no setup step. The first time you schedule a job, `laterr` installs
its background service and tells you where it put it.

Keep the binary where you installed it: the background service runs it from
that path. If you move it, the next `laterr <time> -- ...` repairs the service.

### Uninstall

```sh
laterr uninstall            # stop and remove the background service
laterr uninstall --purge    # ...and delete job history and logs
```

Then delete the binary. Without `--purge`, the database and logs are kept.
If jobs are still pending or running, `uninstall` tells you how many and asks
before continuing.

## Quick start

```sh
laterr +10m -- npm install                 # in 10 minutes
laterr @13:00 -- npm run build             # at the next 13:00
laterr @tomorrow/09:00 -- ./backup.sh      # tomorrow morning

laterr ls                                  # what is waiting or running
laterr log 1                               # what job 1 printed
laterr show 1                              # status, times, exit code
laterr rm 2                                # cancel job 2
laterr kill 3                              # stop job 3, which is running
laterr prune                               # forget jobs finished over 30 days ago
```

The `--` is required. Everything after it is the command, passed through
untouched.

## Time syntax

### Relative: `+<duration>`

| Syntax | Meaning |
|---|---|
| `+30s` | in 30 seconds |
| `+10m` | in 10 minutes |
| `+2h` | in 2 hours |
| `+1d` | in 24 hours |
| `+1h30m` | in 1 hour 30 minutes |

Units are `d`, `h`, `m`, `s`, with whole numbers. They can be combined,
largest first, each at most once: `+1h30m` works, `+30m1h` and `+1h1h` do not.
The total must be greater than zero. The delay is added to the wall clock when
you schedule, so time spent asleep counts.

### Absolute: `@<time>`

| Syntax | Meaning |
|---|---|
| `@13:00` | the next time it is 13:00 (today, or tomorrow if already past) |
| `@13:00:30` | the same, to the second |
| `@tomorrow/09:00` | tomorrow at 09:00 |
| `@2026-10-12/13:30` | on that date at 13:30 |

- The clock is 24-hour: `HH:MM` or `HH:MM:SS`. The hour may be a single digit
  (`@9:00`). Dates are `YYYY-MM-DD`.
- Times are in your local time zone (or `TZ`, if set) at the moment you
  schedule. Changing the system time zone afterwards does not move the job:
  it runs at the same instant, and is simply displayed in the new zone.
- A date and time in the past is an error.
- A time skipped by a daylight saving change is an error. A time that occurs
  twice means the first occurrence.

## Commands

| Command | Purpose |
|---|---|
| [`laterr [options] <time> -- <command> [args...]`](#laterr-options-time----command-args) | Schedule a command |
| [`laterr ls [--all] [--json]`](#laterr-ls---all---json) | List jobs |
| [`laterr show <id>`](#laterr-show-id) | Show the details of a job |
| [`laterr rm <id>...`](#laterr-rm-id) | Cancel pending jobs |
| [`laterr kill <id>`](#laterr-kill-id) | Stop a running job |
| [`laterr log <id> [--follow]`](#laterr-log-id---follow) | Print the output of a job |
| [`laterr prune [--older-than <duration> \| --all]`](#laterr-prune---older-than-duration----all) | Delete finished jobs and their logs |
| [`laterr doctor`](#laterr-doctor) | Check that laterr is set up correctly |
| [`laterr uninstall [--purge]`](#laterr-uninstall---purge) | Remove the background service (and data) |
| `laterr --help`, `laterr --version` | Print the help or the version |

Every command exits with `0` on success and `1` on failure. Error messages
go to stderr.

### `laterr [options] <time> -- <command> [args...]`

Schedule a command.

| Option | Meaning |
|---|---|
| `--grace <duration>` | Skip the job if it would start more than this long after its scheduled time. Default `24h`. Same format as a relative time without the `+`, e.g. `10m`, `1h30m`. |
| `--no-env` | Pass only a minimal environment to the command (see [Environment](#environment)). |
| `--bg` | Do not print the output in this terminal (see [Output in your terminal](#output-in-your-terminal)). |
| `--remind <duration>` | How long before the job runs to announce it in this terminal. Default `5s`. |
| `--no-remind` | Do not announce the job. |

Options go before the `--`. Those that take a value accept both
`--grace 10m` and `--grace=10m`. `--remind` cannot be combined with
`--no-remind` or `--bg`.

The command is executed directly, **not through a shell**. It is looked up in
the `PATH` you had when scheduling. Pipes, redirects, `&&` and globs are not
interpreted; ask for a shell when you need one:

```sh
laterr +10m -- sh -c 'echo hello && npm install > install.log'
```

Stdin is `/dev/null`. Commands that need a terminal will not work.

#### Output in your terminal

Jobs are always run by the background service and their output is always
saved to a log. On top of that, the terminal you scheduled from gets a live
copy:

```console
$ laterr +1h -- npm test
✓ Scheduled job #7
...
$                                             # the prompt is back
laterr: job #7 (npm test) runs in 5 seconds   # 59m55s later
...test output...
laterr: job #7 failed (exit code 1)           # or: succeeded (12.4s)
```

- A heads-up is printed shortly before the job runs, so the output does not
  land in the middle of what you are typing without warning. Change the lead
  time with `--remind 1m`, or turn it off with `--no-remind`. There is no
  heads-up when the job is due sooner than the lead time (`+3s`).
- A last line says how the job ended, so that a command that prints nothing
  still leaves a sign of having run. A job that was `missed` is reported too.
- The job's output goes to stdout; the heads-up and the result line go to
  stderr.
- **Nothing is written while another program has the terminal.** If an
  editor, a pager or any other command is in the foreground when the job
  runs, the output waits and appears once you are back at the prompt. The
  heads-up is skipped in that case.
- **Closing the terminal changes nothing for the job.** It runs anyway; you
  just read the output later with `laterr log <id>`.
- `--bg` keeps the terminal quiet: nothing is printed there, and the output
  goes to the log only.
- When laterr's own output is not a terminal (a pipe, a redirect, a script
  capturing it), it behaves like `--bg`, so scripts are never kept waiting.
- The job itself writes to a log file, not to a terminal. Commands that only
  use colors or progress bars on a terminal will print their plain output.
  Many have a flag to force it (`--color=always`, `FORCE_COLOR=1`).

### `laterr ls [--all] [--json]`

List pending and running jobs. `--all` adds finished ones (`succeeded`,
`failed`, `interrupted`, `cancelled`, `missed`).

```text
ID  WHEN            STATUS   COMMAND
1   Today 13:00     pending  npm install
2   Tomorrow 09:00  pending  npm test
3   Today 12:30     running  npm run build
```

`--json` prints a JSON array for scripts, and nothing else on stdout:

```json
[
  {
    "id": 1,
    "status": "pending",
    "command": ["npm", "install"],
    "cwd": "/Users/me/projects/my-app",
    "created_at": "2026-10-10T03:50:00.000Z",
    "scheduled_at": "2026-10-10T04:00:00.000Z",
    "grace_seconds": 86400,
    "started_at": null,
    "finished_at": null,
    "pid": null,
    "exit_code": null,
    "error_message": null,
    "log_path": "/Users/me/Library/Application Support/laterr/logs/1.log"
  }
]
```

Timestamps are in UTC. `pid` is set while the job is running. Environment
variables are never included.

### `laterr show <id>`

Show one job: status, command, directory, when it was created and scheduled,
grace period, PID while running, start and end times, duration, exit code,
the reason it failed (if any) and the path of its log.

```text
Job #42
  Status     succeeded
  Command    npm install
  Directory  ~/projects/my-app
  Created    Today, 12:50:00
  Scheduled  Today, 13:00
  Grace      24h (not started after Tomorrow, 13:00)
  Started    Today, 13:00:00
  Finished   Today, 13:00:08
  Duration   8.2s
  Exit code  0
  Log        /Users/me/Library/Application Support/laterr/logs/42.log
```

### `laterr rm <id>...`

Cancel pending jobs. They stay in the history as `cancelled` and will never
run. Several IDs can be given: if any of them is not a valid ID, nothing is
cancelled; otherwise each is handled on its own, and the exit code is non-zero
if any of them could not be cancelled. A running job cannot be cancelled: use
`laterr kill`.

### `laterr kill <id>`

Stop a running job. The command and everything it started get `SIGTERM`, and
`SIGKILL` if they are still there 5 seconds later. The job is recorded as
`failed`, with ``stopped with `laterr kill` `` as the reason. The command
returns once the job is gone. A pending job cannot be stopped: use
`laterr rm`.

### `laterr log <id> [--follow]`

Print the job's stdout and stderr. `--follow` (or `-f`) keeps printing until a
running job finishes. For a job that has not run yet, or never will, it says
so instead. Logs are plain files under the data directory and are not deleted
automatically; see `laterr prune`.

### `laterr prune [--older-than <duration> | --all]`

Delete finished jobs and their logs: by default those that ended more than 30
days ago, with `--older-than 7d` those older than that, with `--all` every
finished job. Pending and running jobs are never touched. Job IDs are not
reused.

### `laterr doctor`

Check the installation: binary, data directory and permissions, database,
background service, whether the scheduler answers and whether it is the same
version as the CLI, time zone, and anything that would keep pending jobs from
running (logout on Linux, protected folders on macOS, a working directory that
no longer exists). Exits non-zero if something is broken.

### `laterr uninstall [--purge]`

See [Uninstall](#uninstall).

### Job statuses

| Status | Meaning |
|---|---|
| `pending` | waiting for its time |
| `running` | started, not finished |
| `succeeded` | exited with code 0 |
| `failed` | exited non-zero, was killed (also by `laterr kill`), or could not be started |
| `interrupted` | the scheduler stopped while it was running |
| `cancelled` | removed with `laterr rm` before it started |
| `missed` | its time passed by more than the grace period before it could start |

## What happens when...

| | |
|---|---|
| **I close the terminal** | The job runs anyway: it belongs to the background service, not to your shell. Only the live copy of the output is lost; read it with `laterr log <id>`. |
| **I keep the terminal open** | A heads-up appears 5 seconds before the job runs, then its output. |
| **The computer is asleep at the scheduled time** | The job starts within 30 seconds of waking up. |
| **The computer is off, or I am logged out** | The job starts the next time the scheduler runs (at login), unless that is more than `--grace` (default 24 hours) after the scheduled time. Then it is recorded as `missed` and not run. |
| **I log out or reboot while a job is running** | The job is stopped along with the scheduler and recorded as `interrupted`. It is **not** run again. |
| **A job fails** | It is recorded as `failed` with its exit code. There are no retries. |
| **Several jobs are due at once** | They start in order of scheduled time and run in parallel. A long job never delays another one. |

`laterr` attempts each job **at most once**. When it cannot tell whether a job
completed (for example after a crash), it records `interrupted` rather than
risk running the command twice.

Exact timing is only promised while you are logged in and the machine is
awake. When a job starts late, `laterr show` has the actual start time.

## Environment

By default the job gets a copy of **all environment variables** present when
you scheduled it, like `at` does. This is what makes "works in my terminal"
carry over to "works when scheduled": the same `PATH`, the same version
manager shims, the same credentials.

That copy is stored on disk for as long as the job can still run, so:

- It is erased as soon as the job ends, whether it succeeded, failed, was
  cancelled or was missed. Only pending and running jobs have one.
- The data directory is readable only by you (`0700`, files `0600`).
- Variable values are never printed by `laterr`, not in `show`, `ls` or
  `--json`. Only their count is shown when scheduling.
- With `--no-env`, only `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`,
  `LC_*` and `TMPDIR` are kept:

  ```sh
  laterr --no-env +10m -- npm test
  ```

## Platform notes

| | macOS | Linux |
|---|---|---|
| Background service | launchd LaunchAgent | systemd user service |
| Service definition | `~/Library/LaunchAgents/com.laterr.scheduler.plist` | `$XDG_CONFIG_HOME/systemd/user/laterr.service` (default `~/.config/systemd/user/`) |
| Data and logs | `~/Library/Application Support/laterr/` | `$XDG_DATA_HOME/laterr/` (default `~/.local/share/laterr/`) |
| Scheduler output | `scheduler.log` in the data directory | `journalctl --user -u laterr.service` |

Linux requires systemd with user services (`systemctl --user`). Windows is
not supported.

### macOS: protected folders

macOS guards `~/Documents`, `~/Desktop`, `~/Downloads` and cloud-synced
folders (iCloud Drive, `~/Library/CloudStorage`). A command started by a
background service that touches them either triggers a permission dialog or is
denied, and the job fails.

`laterr` does not try to work around this. It warns when you schedule from
such a folder, and `laterr doctor` lists the affected jobs. To allow access:

- click **Allow** when the dialog appears, or
- add the `laterr` binary under **System Settings > Privacy & Security >
  Full Disk Access** beforehand, or
- keep the project outside those folders (for example `~/projects`).

### Linux: running while logged out

By default systemd stops your user services when your last session ends, so
jobs do not run while you are logged out. `laterr` warns about this when
scheduling. To keep the scheduler running:

```sh
loginctl enable-linger "$USER"
```

## Troubleshooting

Start with `laterr doctor`.

**`Error: background scheduler is unavailable`**
The service could not be started; the message includes what launchd or
systemd reported. On Linux, check that `systemctl --user status` works in
your session. Then look at the scheduler output (see the table above).

**The output did not appear in my terminal**
It is only shown in the terminal you scheduled from, while that terminal is
open, and not with `--bg` or when laterr's output was piped or redirected.
While another program is in the foreground it is held back until the prompt
returns. It is always in `laterr log <id>`.

**The output has no colors**
The job writes to a log file, so commands that check for a terminal turn
colors off. Force them with the command's own option, for example
`--color=always` or `FORCE_COLOR=1`.

**The job failed with "command not found"**
The command is looked up in the `PATH` captured when scheduling, and without
a shell: aliases and shell functions do not exist. Use the real executable,
or `sh -c '...'` / `zsh -ic '...'`.

**The job failed immediately on macOS, or a permission dialog appeared**
See [protected folders](#macos-protected-folders).

**The job was `missed`**
The scheduler was not running (logged out, powered off) until more than the
grace period after the scheduled time. Use a larger `--grace`.

**The job is `interrupted`**
The scheduler stopped while it was running: logout, reboot, `laterr
uninstall`, or a crash. It is not retried; schedule it again if needed.
After a crash on macOS the command itself may still be running: check with
`ps`.

**After upgrading, laterr says the scheduler is a different version**
The old scheduler keeps running until it restarts; scheduling a job and
`doctor` both point this out. `doctor` prints the command to restart it; note
that this interrupts running jobs.

**`the database was created by a newer version of laterr`**
An older `laterr` binary is being used on data written by a newer one. Use the
newer binary; if the message comes from the scheduler, restart it.

## How it works

```text
 laterr <time> -- cmd ──► SQLite database ◄── laterr ls / show / rm / log ...
          │                     ▲
          │ wake                │ read due jobs, record results
          ▼                     │
     Unix socket ────────► scheduler (laterr daemon) ──► runs cmd ──► logs/<id>.log
                           launchd / systemd user service
```

- The **CLI** resolves the time expression, records the job (command,
  directory, environment, time) in a SQLite database, and nudges the scheduler
  over a Unix socket.
- The **scheduler** is the same binary, run as `laterr daemon` by a per-user
  launchd or systemd service. It sleeps until the next job is due, never for
  more than 30 seconds at a time, so that a job is not late after the machine
  wakes up or the clock changes. It marks a job `running` *before* starting
  it, which is what makes execution at-most-once.
- Each job runs in its own process group, with its output appended to its own
  log file. Stopping a job stops everything it started.
- The **follower** is a small process left behind in your terminal. It only
  relays the log to the terminal; it has no part in running the job.

Everything lives in one data directory, readable only by you:

| File | Purpose |
|---|---|
| `laterr.db` | job database (SQLite, WAL mode) |
| `logs/<id>.log` | stdout and stderr of each job |
| `laterr.sock` | socket the CLI uses to reach the scheduler |
| `scheduler.lock` | held by the running scheduler; there is one per data directory |
| `scheduler.log` | the scheduler's own output (macOS) |

[CONCEPT.md](CONCEPT.md) (in Japanese) has the full specification and the
reasoning behind these choices.

## Development

Requires Go 1.26 or later. `laterr` is pure Go, SQLite included, and builds
without CGO.

```sh
go test ./...                                   # unit and integration tests
go vet ./... && gofmt -l .                      # what CI checks
CGO_ENABLED=0 go build -o laterr ./cmd/laterr
```

| Package | Responsibility |
|---|---|
| `cmd/laterr` | entry point, and the integration tests that drive a real scheduler |
| `internal/cli` | argument parsing, the commands, output formatting, the follower |
| `internal/timespec` | the time and duration syntax |
| `internal/store` | jobs and their status transitions, in SQLite |
| `internal/scheduler` | the daemon: timing, running jobs, the socket protocol |
| `internal/service` | launchd and systemd integration |
| `internal/paths` | where the files live |

Setting `LATERR_HOME` points `laterr` at another data directory and disables
all service management, which is how the integration tests run a scheduler
without touching your real one:

```sh
export LATERR_HOME=/tmp/laterr-dev
laterr daemon &            # the scheduler, in the foreground
laterr +5s -- echo hello
```

Keep that path short: it holds a Unix socket, whose address is limited to
about 100 bytes.

Pushing a tag such as `v0.1.0` builds the release archives for macOS and
Linux (arm64 and amd64) and publishes them with their checksums.

## Contributing

Issues and pull requests are welcome. Before opening a pull request, please
make sure `gofmt`, `go vet ./...` and `go test ./...` pass; CI runs them on
macOS and Linux.

`laterr` is deliberately small. Recurring schedules, retries, remote hosts and
Windows support are out of scope; see the non-goals in
[CONCEPT.md](CONCEPT.md).

## License

[MIT](LICENSE)
