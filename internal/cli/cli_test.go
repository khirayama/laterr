package cli

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseSchedule(t *testing.T) {
	got, err := parseSchedule([]string{"--no-env", "--grace", "1h30m", "+10m", "--", "sh", "-c", "echo hi", "--", "--grace"})
	if err != nil {
		t.Fatal(err)
	}
	want := scheduleArgs{timeExpr: "+10m", grace: 90 * time.Minute, remind: 10 * time.Second, noEnv: true, argv: []string{"sh", "-c", "echo hi", "--", "--grace"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Options may follow the time, and the default grace is 24 hours.
	got, err = parseSchedule([]string{"@13:00", "--grace=10m", "--", "ls"})
	if err != nil || got.timeExpr != "@13:00" || got.grace != 10*time.Minute || got.noEnv {
		t.Errorf("got %+v, %v", got, err)
	}
	got, err = parseSchedule([]string{"+1d", "--", "ls"})
	if err != nil || got.grace != 24*time.Hour {
		t.Errorf("got %+v, %v", got, err)
	}

	// Reminders: 10 seconds by default, adjustable, and off without a terminal.
	for args, want := range map[string]time.Duration{
		"+1h -- ls":                  10 * time.Second,
		"--remind 1m +1h -- ls":      time.Minute,
		"--remind=30s +1h -- ls":     30 * time.Second,
		"--no-remind +1h -- ls":      0,
		"--bg +1h -- ls":             0,
		"--bg --no-remind +1h -- ls": 0,
	} {
		got, err := parseSchedule(strings.Fields(args))
		if err != nil || got.remind != want || got.bg != strings.Contains(args, "--bg") {
			t.Errorf("%q: got %+v, %v", args, got, err)
		}
	}

	for args, want := range map[string]string{
		"--remind 10x +1h -- ls":            "invalid duration for --remind: 10x",
		"--remind":                          "--remind needs a duration",
		"--bg --remind 5s +1h -- ls":        "--remind has no effect with --bg",
		"--remind 5s --no-remind +1h -- ls": "--remind and --no-remind cannot be combined",
		"--foreground +1h -- ls":            "unknown option: --foreground",
		"+10m ls":                           "unexpected argument: ls",
		"+10m":                              "missing `--`",
		"+10m --":                           "no command given",
		"-- ls":                             "missing time",
		"--grace":                           "--grace needs a duration",
		"--grace 0s +1m -- ls":              "invalid duration for --grace: 0s",
		"--grace +5m +1m -- ls":             "invalid duration for --grace: +5m",
		"-x +1m -- ls":                      "unknown option: -x",
		"+1m +2m -- ls":                     "unexpected argument: +2m",
	} {
		_, err := parseSchedule(strings.Fields(args))
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", args, err, want)
		}
	}
	if _, err := parseSchedule([]string{"+1m", "--", ""}); err == nil {
		t.Error("an empty command name should be rejected")
	}
}

func TestTimeHint(t *testing.T) {
	for arg, want := range map[string]string{
		"10m": "+10m", "1h30m": "+1h30m", "13:00": "@13:00", "tomorrow/09:00": "@tomorrow/09:00",
		"2020-01-01/10:00": "@2020-01-01/10:00",
		"frobnicate":       "", "10x": "", "status": "", "25:00": "",
	} {
		if got := timeHint(arg); got != want {
			t.Errorf("timeHint(%q) = %q, want %q", arg, got, want)
		}
	}
}

func TestMinimalEnv(t *testing.T) {
	got := minimalEnv([]string{
		"PATH=/bin", "SECRET=x", "HOME=/h", "LC_ALL=C", "LC_CTYPE=UTF-8", "LANG=en", "LANGUAGE=en",
		"USER=u", "LOGNAME=u", "SHELL=/bin/sh", "TMPDIR=/t", "PATHX=1", "AWS_SECRET_ACCESS_KEY=k", "EMPTY=",
	})
	want := []string{"PATH=/bin", "HOME=/h", "LC_ALL=C", "LC_CTYPE=UTF-8", "LANG=en", "USER=u", "LOGNAME=u", "SHELL=/bin/sh", "TMPDIR=/t"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestWhen(t *testing.T) {
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()

	now := time.Date(2026, 10, 9, 12, 44, 0, 0, time.UTC)
	cases := []struct {
		t       time.Time
		seconds bool
		want    string
	}{
		{time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC), false, "Today, 13:00"},
		{time.Date(2026, 10, 9, 13, 0, 1, 0, time.UTC), false, "Today, 13:00:01"},
		{time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC), true, "Today, 13:00:00"},
		{time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), false, "Today, 00:00"},
		{time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), false, "Tomorrow, 00:00"},
		{time.Date(2026, 10, 10, 23, 59, 0, 0, time.UTC), false, "Tomorrow, 23:59"},
		{time.Date(2026, 10, 8, 23, 59, 0, 0, time.UTC), false, "Yesterday, 23:59"},
		{time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC), false, "Oct 11, 09:00"},
		{time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC), false, "Oct 7, 09:00"},
		{time.Date(2027, 1, 2, 9, 0, 0, 0, time.UTC), false, "Jan 2 2027, 09:00"},
	}
	for _, c := range cases {
		if got := when(c.t, now, ", ", c.seconds); got != c.want {
			t.Errorf("%v: got %q, want %q", c.t, got, c.want)
		}
	}
	// "Tomorrow" follows the calendar, not a 24-hour distance.
	lateNow := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	if got := when(time.Date(2026, 11, 1, 0, 1, 0, 0, time.UTC), lateNow, " ", false); got != "Tomorrow 00:01" {
		t.Errorf("got %q", got)
	}
}

func TestDurations(t *testing.T) {
	for d, want := range map[time.Duration]string{
		999 * time.Millisecond:            "1 second",
		2 * time.Second:                   "2 seconds",
		16*time.Minute - time.Millisecond: "16 minutes",
		time.Minute:                       "1 minute",
		time.Hour:                         "1 hour",
		90 * time.Minute:                  "1 hour 30 minutes",
		24 * time.Hour:                    "1 day",
		50 * time.Hour:                    "2 days 2 hours",
	} {
		if got := untilText(d); got != want {
			t.Errorf("untilText(%v) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		24 * time.Hour:     "24h",
		90 * time.Minute:   "1h30m",
		10 * time.Minute:   "10m",
		time.Second:        "1s",
		3661 * time.Second: "1h1m1s",
	} {
		if got := compactDuration(d); got != want {
			t.Errorf("compactDuration(%v) = %q, want %q", d, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		8200 * time.Millisecond: "8.2s",
		65 * time.Second:        "1m 5s",
		3723 * time.Second:      "1h 2m 3s",
	} {
		if got := elapsed(d); got != want {
			t.Errorf("elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestCommandLine(t *testing.T) {
	for want, argv := range map[string][]string{
		"npm install":                   {"npm", "install"},
		"./backup.sh --to=/mnt/x":       {"./backup.sh", "--to=/mnt/x"},
		"sh -c 'echo hello && npm i'":   {"sh", "-c", "echo hello && npm i"},
		`echo 'it'\''s' '' '$HOME' '*'`: {"echo", "it's", "", "$HOME", "*"},
	} {
		if got := commandLine(argv); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	if got := oneLine("a\nb\tc", 60); got != `a\nb\tc` {
		t.Errorf("got %q", got)
	}
	if got := oneLine(strings.Repeat("é", 100), 10); len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("got %q", got)
	}
}
