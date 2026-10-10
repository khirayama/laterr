// Package timespec parses laterr's time expressions. It has no dependency on
// the rest of the program so that it can be tested in isolation.
package timespec

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrPast is returned when an explicit absolute time is not in the future.
var ErrPast = errors.New("scheduled time is in the past")

// Parse resolves a time expression ("+10m", "@13:00", "@tomorrow/09:00",
// "@2026-10-12/13:30") to an instant, relative to now and interpreted in loc.
func Parse(expr string, now time.Time, loc *time.Location) (time.Time, error) {
	now = now.Round(0) // drop the monotonic reading: schedules follow the wall clock
	switch {
	case strings.HasPrefix(expr, "+"):
		d, err := parseDuration(expr[1:])
		if err != nil {
			return time.Time{}, invalid(expr, err)
		}
		return now.Add(d), nil
	case strings.HasPrefix(expr, "@"):
		t, err := parseAbsolute(expr[1:], now, loc)
		if err != nil {
			if errors.Is(err, ErrPast) {
				return time.Time{}, err
			}
			return time.Time{}, invalid(expr, err)
		}
		return t, nil
	}
	return time.Time{}, invalid(expr, errors.New("must start with + or @"))
}

// ParseDuration parses a duration such as "10m" or "1h30m": the relative time
// syntax without the leading "+".
func ParseDuration(s string) (time.Duration, error) {
	d, err := parseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s%s", s, reason(err))
	}
	return d, nil
}

// errNoSuchTime reports a wall-clock time inside a DST gap.
var errNoSuchTime = errors.New("that local time does not exist because of a daylight saving change")

// errSyntax marks errors that need no explanation beyond echoing the input.
var errSyntax = errors.New("syntax")

func reason(err error) string {
	if err == errSyntax {
		return ""
	}
	return " (" + err.Error() + ")"
}

func invalid(expr string, err error) error {
	return fmt.Errorf("invalid time expression: %s%s", expr, reason(err))
}

const unitOrder = "dhms"

var unitSeconds = [...]int64{86400, 3600, 60, 1}

func parseDuration(s string) (time.Duration, error) {
	const maxSeconds = math.MaxInt64 / int64(time.Second)
	if s == "" {
		return 0, errSyntax
	}
	var total int64
	lastRank := -1
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i || j == len(s) {
			return 0, errSyntax
		}
		n, err := strconv.ParseInt(s[i:j], 10, 64)
		if err != nil {
			return 0, errors.New("value is too large")
		}
		rank := strings.IndexByte(unitOrder, s[j])
		if rank < 0 {
			return 0, errSyntax
		}
		if rank == lastRank {
			return 0, fmt.Errorf("unit %q is used more than once", s[j])
		}
		if rank < lastRank {
			return 0, errors.New("units must go from largest to smallest: d, h, m, s")
		}
		if n > (maxSeconds-total)/unitSeconds[rank] {
			return 0, errors.New("value is too large")
		}
		total += n * unitSeconds[rank]
		lastRank = rank
		i = j + 1
	}
	if total == 0 {
		return 0, errors.New("must be greater than zero")
	}
	return time.Duration(total) * time.Second, nil
}

func parseAbsolute(body string, now time.Time, loc *time.Location) (time.Time, error) {
	datePart, clockPart, hasDate := "", body, false
	if i := strings.IndexByte(body, '/'); i >= 0 {
		datePart, clockPart, hasDate = body[:i], body[i+1:], true
	}
	h, m, s, ok := parseClock(clockPart)
	if !ok {
		return time.Time{}, errSyntax
	}
	y, mo, d := now.In(loc).Date()

	switch {
	case !hasDate:
		// The next occurrence of this wall-clock time: today if it is still
		// ahead, otherwise tomorrow.
		today := candidates(y, mo, d, h, m, s, loc)
		for _, t := range today {
			if t.After(now) {
				return t, nil
			}
		}
		// Today's occurrence may have been skipped by a DST gap that is still
		// ahead of us. Silently pushing it a full day would be surprising.
		if len(today) == 0 && time.Date(y, mo, d, h, m, s, 0, loc).After(now) {
			return time.Time{}, errNoSuchTime
		}
		ty, tmo, td := nextDay(y, mo, d)
		return earliest(candidates(ty, tmo, td, h, m, s, loc))

	case datePart == "tomorrow":
		ty, tmo, td := nextDay(y, mo, d)
		return earliest(candidates(ty, tmo, td, h, m, s, loc))

	default:
		dy, dmo, dd, ok := parseDate(datePart)
		if !ok {
			return time.Time{}, errSyntax
		}
		if n := time.Date(dy, dmo, dd, 0, 0, 0, 0, time.UTC); n.Year() != dy || n.Month() != dmo || n.Day() != dd {
			return time.Time{}, errors.New("no such date")
		}
		t, err := earliest(candidates(dy, dmo, dd, h, m, s, loc))
		if err != nil {
			return time.Time{}, err
		}
		if !t.After(now) {
			return time.Time{}, ErrPast
		}
		return t, nil
	}
}

// earliest picks among the instants at which a wall-clock time occurs. For a
// time repeated by a DST change the earlier instant wins; a time skipped by
// one does not exist.
func earliest(c []time.Time) (time.Time, error) {
	if len(c) == 0 {
		return time.Time{}, errNoSuchTime
	}
	return c[0], nil
}

func nextDay(y int, mo time.Month, d int) (int, time.Month, int) {
	return time.Date(y, mo, d+1, 0, 0, 0, 0, time.UTC).Date()
}

// candidates returns every instant at which the clocks in loc show the given
// wall-clock time, earliest first: none inside a DST gap, two in a DST overlap.
// time.Date cannot be used for this because it silently normalises gaps and
// does not say which side of an overlap it picked.
func candidates(y int, mo time.Month, d, h, m, s int, loc *time.Location) []time.Time {
	naive := time.Date(y, mo, d, h, m, s, 0, time.UTC)
	// UTC offsets are within ±14h, so the real instant lies well inside this
	// window and the offsets in force at its edges cover any transition near it.
	_, before := naive.Add(-26 * time.Hour).In(loc).Zone()
	_, after := naive.Add(26 * time.Hour).In(loc).Zone()
	offsets := []int{before}
	if after != before {
		offsets = append(offsets, after)
	}
	var out []time.Time
	for _, off := range offsets {
		t := naive.Add(-time.Duration(off) * time.Second).In(loc)
		ty, tmo, td := t.Date()
		if ty == y && tmo == mo && td == d && t.Hour() == h && t.Minute() == m && t.Second() == s {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// parseClock parses HH:MM or HH:MM:SS.
func parseClock(s string) (h, m, sec int, ok bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, 0, 0, false
	}
	var v [3]int
	for i, p := range parts {
		if len(p) != 2 && !(i == 0 && len(p) == 1) {
			return 0, 0, 0, false
		}
		n, ok := digits(p)
		if !ok {
			return 0, 0, 0, false
		}
		v[i] = n
	}
	if v[0] > 23 || v[1] > 59 || v[2] > 59 {
		return 0, 0, 0, false
	}
	return v[0], v[1], v[2], true
}

// parseDate parses YYYY-MM-DD without checking that the day exists.
func parseDate(s string) (y int, mo time.Month, d int, ok bool) {
	parts := strings.Split(s, "-")
	if len(parts) != 3 || len(parts[0]) != 4 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return 0, 0, 0, false
	}
	y, ok1 := digits(parts[0])
	m, ok2 := digits(parts[1])
	d, ok3 := digits(parts[2])
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, 0, false
	}
	return y, time.Month(m), d, true
}

func digits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}
