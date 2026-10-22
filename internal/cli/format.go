package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// dayLabel names the calendar day of t relative to now: "Today", "Tomorrow",
// "Yesterday", or a date.
func dayLabel(t, now time.Time) string {
	midnight := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	switch midnight(t).Sub(midnight(now)) {
	case 0:
		return "Today"
	case 24 * time.Hour:
		return "Tomorrow"
	case -24 * time.Hour:
		return "Yesterday"
	}
	if t.Year() == now.Year() {
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2 2006")
}

// when formats t in the local time zone, e.g. "Today, 13:00". Seconds are
// shown if they are non-zero or if seconds is set.
func when(t, now time.Time, sep string, seconds bool) string {
	t, now = t.Local(), now.Local()
	layout := "15:04"
	if seconds || t.Second() != 0 {
		layout = "15:04:05"
	}
	return dayLabel(t, now) + sep + t.Format(layout)
}

func plural(n int64, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// untilText describes a wait in words, e.g. "16 minutes" or "1 hour 30 minutes".
func untilText(d time.Duration) string {
	s := int64(d.Round(time.Second) / time.Second)
	switch {
	case s < 60:
		return plural(s, "second")
	case s < 3600:
		return plural(s/60, "minute")
	case s < 86400:
		if m := s % 3600 / 60; m != 0 {
			return plural(s/3600, "hour") + " " + plural(m, "minute")
		}
		return plural(s/3600, "hour")
	}
	if h := s % 86400 / 3600; h != 0 {
		return plural(s/86400, "day") + " " + plural(h, "hour")
	}
	return plural(s/86400, "day")
}

// compactDuration formats a whole number of seconds like the --grace syntax,
// e.g. "24h" or "1h30m".
func compactDuration(d time.Duration) string {
	s := int64(d / time.Second)
	var b strings.Builder
	if h := s / 3600; h != 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m := s % 3600 / 60; m != 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if sec := s % 60; sec != 0 || b.Len() == 0 {
		fmt.Fprintf(&b, "%ds", sec)
	}
	return b.String()
}

// elapsed formats how long a job ran, e.g. "8.2s" or "1h 2m 3s".
func elapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	s := int64(d.Round(time.Second) / time.Second)
	if s < 3600 {
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %dm %ds", s/3600, s%3600/60, s%60)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// commandLine renders argv so that it reads unambiguously: arguments with
// spaces or shell metacharacters are quoted the way a shell would need them.
func commandLine(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		if shellSafe.MatchString(arg) {
			parts[i] = arg
		} else {
			parts[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}

// shortCommand renders argv for a table cell or a one-line message.
func shortCommand(argv []string) string {
	return oneLine(commandLine(argv), 60)
}

// oneLine makes s safe for a table cell: no line breaks, bounded length.
func oneLine(s string, limit int) string {
	s = strings.NewReplacer("\n", "\\n", "\r", "\\r", "\t", "\\t").Replace(s)
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return s
}

// tildify abbreviates the home directory as "~".
func tildify(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return path
}
