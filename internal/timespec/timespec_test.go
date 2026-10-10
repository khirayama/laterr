package timespec

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("time zone %s unavailable: %v", name, err)
	}
	return loc
}

func TestParseRelative(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 44, 10, 0, time.UTC)
	cases := map[string]time.Duration{
		"+30s":        30 * time.Second,
		"+600s":       600 * time.Second,
		"+10m":        10 * time.Minute,
		"+2h":         2 * time.Hour,
		"+1d":         24 * time.Hour,
		"+1h30m":      90 * time.Minute,
		"+1d2h3m4s":   26*time.Hour + 3*time.Minute + 4*time.Second,
		"+0h10m":      10 * time.Minute,
		"+90m":        90 * time.Minute,
		"+1d30s":      24*time.Hour + 30*time.Second,
		"+000000010m": 10 * time.Minute,
	}
	for expr, want := range cases {
		got, err := Parse(expr, now, time.UTC)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", expr, err)
			continue
		}
		if !got.Equal(now.Add(want)) {
			t.Errorf("%s: got %v, want %v", expr, got, now.Add(want))
		}
	}
}

func TestParseRelativeErrors(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, expr := range []string{
		"+", "+10", "+10x", "+m", "+-10m", "+1.5h", "+10 m", "+0s", "+0h0m",
		"+30m1h", "+1h1h", "+1s1m", "+10M", "+1h30", "10m", "",
		"+99999999999999999999s", "+9223372036854775807d", "+200000000000d",
	} {
		if _, err := Parse(expr, now, time.UTC); err == nil {
			t.Errorf("%q: expected an error", expr)
		} else if !strings.HasPrefix(err.Error(), "invalid time expression: "+expr) {
			t.Errorf("%q: unexpected message: %v", expr, err)
		}
	}
	// The spec's example message, verbatim.
	if _, err := Parse("+10x", now, time.UTC); err.Error() != "invalid time expression: +10x" {
		t.Errorf("got %q", err)
	}
}

func TestParseDuration(t *testing.T) {
	if d, err := ParseDuration("1h30m"); err != nil || d != 90*time.Minute {
		t.Errorf("got %v, %v", d, err)
	}
	for _, s := range []string{"", "+10m", "10x", "0m", "10"} {
		if _, err := ParseDuration(s); err == nil {
			t.Errorf("%q: expected an error", s)
		}
	}
	if _, err := ParseDuration("10x"); err.Error() != "10x" {
		t.Errorf("got %q", err)
	}
}

func TestParseClockOnly(t *testing.T) {
	loc := mustLoad(t, "Asia/Tokyo")
	at := func(y int, mo time.Month, d, h, m, s int) time.Time {
		return time.Date(y, mo, d, h, m, s, 0, loc)
	}
	cases := []struct {
		now  time.Time
		expr string
		want time.Time
	}{
		{at(2026, 10, 9, 12, 44, 0), "@13:00", at(2026, 10, 9, 13, 0, 0)},
		{at(2026, 10, 9, 12, 44, 0), "@13:00:30", at(2026, 10, 9, 13, 0, 30)},
		{at(2026, 10, 9, 12, 44, 0), "@9:05", at(2026, 10, 10, 9, 5, 0)},
		// Exactly now is not in the future, so it rolls over to tomorrow.
		{at(2026, 10, 9, 13, 0, 0), "@13:00", at(2026, 10, 10, 13, 0, 0)},
		{at(2026, 10, 9, 13, 0, 1), "@13:00", at(2026, 10, 10, 13, 0, 0)},
		// Crossing midnight, a month end, a leap day and a year end.
		{at(2026, 10, 9, 23, 59, 59), "@00:00", at(2026, 10, 10, 0, 0, 0)},
		{at(2026, 10, 31, 18, 0, 0), "@09:00", at(2026, 11, 1, 9, 0, 0)},
		{at(2028, 2, 28, 18, 0, 0), "@09:00", at(2028, 2, 29, 9, 0, 0)},
		{at(2027, 2, 28, 18, 0, 0), "@09:00", at(2027, 3, 1, 9, 0, 0)},
		{at(2026, 12, 31, 18, 0, 0), "@09:00", at(2027, 1, 1, 9, 0, 0)},
	}
	for _, c := range cases {
		got, err := Parse(c.expr, c.now, loc)
		if err != nil {
			t.Errorf("%s at %v: %v", c.expr, c.now, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("%s at %v: got %v, want %v", c.expr, c.now, got, c.want)
		}
		if !got.After(c.now) {
			t.Errorf("%s at %v: result %v is not in the future", c.expr, c.now, got)
		}
	}
}

func TestParseDated(t *testing.T) {
	loc := mustLoad(t, "Asia/Tokyo")
	now := time.Date(2026, 10, 9, 12, 44, 0, 0, loc)

	got, err := Parse("@tomorrow/09:00", now, loc)
	if want := time.Date(2026, 10, 10, 9, 0, 0, 0, loc); err != nil || !got.Equal(want) {
		t.Errorf("tomorrow: got %v, %v", got, err)
	}
	got, err = Parse("@tomorrow/09:00", time.Date(2026, 10, 31, 23, 0, 0, 0, loc), loc)
	if want := time.Date(2026, 11, 1, 9, 0, 0, 0, loc); err != nil || !got.Equal(want) {
		t.Errorf("tomorrow at month end: got %v, %v", got, err)
	}
	got, err = Parse("@2026-10-12/13:30", now, loc)
	if want := time.Date(2026, 10, 12, 13, 30, 0, 0, loc); err != nil || !got.Equal(want) {
		t.Errorf("date: got %v, %v", got, err)
	}
	got, err = Parse("@2028-02-29/00:00:01", now, loc)
	if want := time.Date(2028, 2, 29, 0, 0, 1, 0, loc); err != nil || !got.Equal(want) {
		t.Errorf("leap day: got %v, %v", got, err)
	}
	// Later today is fine; earlier today and exactly now are in the past.
	if _, err := Parse("@2026-10-09/12:45", now, loc); err != nil {
		t.Errorf("later today: %v", err)
	}
	for _, expr := range []string{"@2026-10-09/12:44", "@2026-10-09/09:00", "@2020-01-01/00:00"} {
		if _, err := Parse(expr, now, loc); !errors.Is(err, ErrPast) {
			t.Errorf("%s: got %v, want ErrPast", expr, err)
		}
	}
	if ErrPast.Error() != "scheduled time is in the past" {
		t.Errorf("unexpected message %q", ErrPast)
	}
}

func TestParseAbsoluteErrors(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, expr := range []string{
		"@", "@13", "@13:0", "@24:00", "@13:60", "@13:00:60", "@13:00:00:00", "@1300",
		"@ 13:00", "@13:00 ", "@-1:00", "@tomorrow", "@tomorrow/", "@/13:00", "@today/13:00",
		"@Tomorrow/09:00", "@2026-10-12", "@2026-10-12/", "@2026-13-01/10:00",
		"@2027-02-29/10:00", "@2026-04-31/10:00", "@2026-1-5/10:00", "@26-10-12/10:00",
		"@2026/10/12/10:00", "@2026-10-12T13:30", "@2026-10-12/13:30+09:00",
	} {
		_, err := Parse(expr, now, time.UTC)
		if err == nil {
			t.Errorf("%q: expected an error", expr)
		} else if !strings.HasPrefix(err.Error(), "invalid time expression: "+expr) {
			t.Errorf("%q: unexpected message: %v", expr, err)
		}
	}
}

// America/New_York in 2026: clocks jump 02:00 -> 03:00 on March 8 and fall
// back 02:00 -> 01:00 on November 1.
func TestParseDST(t *testing.T) {
	loc := mustLoad(t, "America/New_York")
	edt := time.FixedZone("EDT", -4*3600)
	est := time.FixedZone("EST", -5*3600)

	t.Run("gap", func(t *testing.T) {
		now := time.Date(2026, 3, 7, 12, 0, 0, 0, est)
		for _, expr := range []string{"@2026-03-08/02:30", "@tomorrow/02:30", "@2026-03-08/02:00"} {
			if _, err := Parse(expr, now, loc); err == nil || errors.Is(err, ErrPast) {
				t.Errorf("%s: got %v, want a nonexistent-time error", expr, err)
			}
		}
		// Either side of the gap is fine.
		got, err := Parse("@2026-03-08/01:59:59", now, loc)
		if want := time.Date(2026, 3, 8, 1, 59, 59, 0, est); err != nil || !got.Equal(want) {
			t.Errorf("before gap: got %v, %v", got, err)
		}
		got, err = Parse("@2026-03-08/03:00", now, loc)
		if want := time.Date(2026, 3, 8, 3, 0, 0, 0, edt); err != nil || !got.Equal(want) {
			t.Errorf("after gap: got %v, %v", got, err)
		}
		// A bare clock time whose next occurrence falls into the gap.
		if _, err := Parse("@02:30", time.Date(2026, 3, 8, 1, 0, 0, 0, est), loc); err == nil {
			t.Error("@02:30 before the gap: expected an error")
		}
		if _, err := Parse("@02:30", time.Date(2026, 3, 7, 12, 0, 0, 0, est), loc); err == nil {
			t.Error("@02:30 the day before the gap: expected an error")
		}
		// After the gap has passed, today's 02:30 is behind us: use tomorrow's.
		got, err = Parse("@02:30", time.Date(2026, 3, 8, 4, 0, 0, 0, edt), loc)
		if want := time.Date(2026, 3, 9, 2, 30, 0, 0, edt); err != nil || !got.Equal(want) {
			t.Errorf("after the gap: got %v, %v", got, err)
		}
	})

	t.Run("overlap", func(t *testing.T) {
		first := time.Date(2026, 11, 1, 1, 30, 0, 0, edt)
		second := time.Date(2026, 11, 1, 1, 30, 0, 0, est)

		// An explicit date takes the earlier of the two instants.
		got, err := Parse("@2026-11-01/01:30", time.Date(2026, 10, 31, 12, 0, 0, 0, edt), loc)
		if err != nil || !got.Equal(first) {
			t.Errorf("explicit: got %v, %v; want %v", got, err, first)
		}
		got, err = Parse("@tomorrow/01:30", time.Date(2026, 10, 31, 12, 0, 0, 0, edt), loc)
		if err != nil || !got.Equal(first) {
			t.Errorf("tomorrow: got %v, %v; want %v", got, err, first)
		}
		// A bare clock time means "the next 01:30 to come".
		got, err = Parse("@01:30", time.Date(2026, 11, 1, 0, 30, 0, 0, edt), loc)
		if err != nil || !got.Equal(first) {
			t.Errorf("before both: got %v, %v; want %v", got, err, first)
		}
		got, err = Parse("@01:30", time.Date(2026, 11, 1, 1, 45, 0, 0, edt), loc)
		if err != nil || !got.Equal(second) {
			t.Errorf("between: got %v, %v; want %v", got, err, second)
		}
		got, err = Parse("@01:30", time.Date(2026, 11, 1, 1, 45, 0, 0, est), loc)
		if want := time.Date(2026, 11, 2, 1, 30, 0, 0, est); err != nil || !got.Equal(want) {
			t.Errorf("after both: got %v, %v; want %v", got, err, want)
		}
		// The earlier instant has passed, so the explicit form is in the past.
		if _, err := Parse("@2026-11-01/01:30", time.Date(2026, 11, 1, 1, 45, 0, 0, edt), loc); !errors.Is(err, ErrPast) {
			t.Errorf("explicit after first: got %v, want ErrPast", err)
		}
	})

	t.Run("day length", func(t *testing.T) {
		// "+1d" is 24 hours, not "same clock time tomorrow".
		now := time.Date(2026, 3, 7, 12, 0, 0, 0, est)
		got, _ := Parse("+1d", now, loc)
		if got.Sub(now) != 24*time.Hour || got.In(loc).Hour() != 13 {
			t.Errorf("got %v", got.In(loc))
		}
		// "@12:00" tomorrow across the change is only 23 hours away.
		got, _ = Parse("@12:00", now, loc)
		if got.Sub(now) != 23*time.Hour {
			t.Errorf("got %v after %v", got, got.Sub(now))
		}
	})
}

// Lord Howe Island shifts by 30 minutes, and Southern Hemisphere transitions
// run the other way round within the year.
func TestParseDSTHalfHour(t *testing.T) {
	loc := mustLoad(t, "Australia/Lord_Howe")
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// 2026-10-04 02:00 +10:30 -> 02:30 +11.
	if _, err := Parse("@2026-10-04/02:15", now, loc); err == nil {
		t.Error("02:15 should not exist")
	}
	if _, err := Parse("@2026-10-04/02:30", now, loc); err != nil {
		t.Errorf("02:30 should exist: %v", err)
	}
}

func TestResultIndependentOfLocationAfterParsing(t *testing.T) {
	tokyo := mustLoad(t, "Asia/Tokyo")
	now := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC) // 12:00 in Tokyo
	got, err := Parse("@13:00", now, tokyo)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}
