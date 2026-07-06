package cron

import (
	"strings"
	"testing"
	"time"
)

func bits(values ...int) uint64 {
	var mask uint64
	for _, v := range values {
		mask |= 1 << v
	}
	return mask
}

func rangeBits(lo, hi int) uint64 {
	var mask uint64
	for v := lo; v <= hi; v++ {
		mask |= 1 << v
	}
	return mask
}

func TestParseField(t *testing.T) {
	tests := []struct {
		spec   string
		lo, hi int
		want   uint64
	}{
		{"*", 0, 59, rangeBits(0, 59)},
		{"*", 1, 12, rangeBits(1, 12)},
		{"0", 0, 59, bits(0)},
		{"5", 0, 59, bits(5)},
		{"59", 0, 59, bits(59)},
		{"7", 0, 7, bits(7)},
		{"1-3", 0, 59, bits(1, 2, 3)},
		{"1-1", 0, 59, bits(1)},
		{"0-59", 0, 59, rangeBits(0, 59)},
		{"*/15", 0, 59, bits(0, 15, 30, 45)},
		{"*/30", 0, 59, bits(0, 30)},
		{"*/1", 0, 23, rangeBits(0, 23)},
		{"10-40/10", 0, 59, bits(10, 20, 30, 40)},
		{"1-5/2", 0, 59, bits(1, 3, 5)},
		{"5/20", 0, 59, bits(5, 25, 45)},
		{"0,30", 0, 59, bits(0, 30)},
		{"1,2,3", 0, 59, bits(1, 2, 3)},
		{"1-2,50-51", 0, 59, bits(1, 2, 50, 51)},
		{"*/20,7", 0, 59, bits(0, 7, 20, 40)},
		{"31", 1, 31, bits(31)},
	}
	for _, tt := range tests {
		got, err := parseField(tt.spec, tt.lo, tt.hi)
		if err != nil {
			t.Errorf("parseField(%q, %d, %d): unexpected error: %v", tt.spec, tt.lo, tt.hi, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseField(%q, %d, %d) = %b, want %b", tt.spec, tt.lo, tt.hi, got, tt.want)
		}
	}
}

func TestParseFieldErrors(t *testing.T) {
	tests := []struct {
		spec   string
		lo, hi int
	}{
		{"60", 0, 59},
		{"24", 0, 23},
		{"0", 1, 31},
		{"32", 1, 31},
		{"13", 1, 12},
		{"8", 0, 7},
		{"-1", 0, 59},
		{"1-", 0, 59},
		{"-", 0, 59},
		{"5-1", 0, 59},
		{"1-70", 0, 59},
		{"*/0", 0, 59},
		{"*/-2", 0, 59},
		{"*/x", 0, 59},
		{"1//2", 0, 59},
		{"a", 0, 59},
		{"", 0, 59},
		{",", 0, 59},
		{"1,", 0, 59},
		{"1.5", 0, 59},
		{"1 2", 0, 59},
	}
	for _, tt := range tests {
		if _, err := parseField(tt.spec, tt.lo, tt.hi); err == nil {
			t.Errorf("parseField(%q, %d, %d): expected error, got none", tt.spec, tt.lo, tt.hi)
		}
	}
}

func TestParseScheduleFieldCount(t *testing.T) {
	for _, fields := range [][]string{nil, {"*"}, {"*", "*", "*", "*"}, {"*", "*", "*", "*", "*", "*"}} {
		if _, err := ParseSchedule(fields); err == nil {
			t.Errorf("ParseSchedule(%v): expected error, got none", fields)
		}
	}
}

func TestParseScheduleErrorNamesField(t *testing.T) {
	tests := []struct {
		fields []string
		want   string
	}{
		{[]string{"60", "*", "*", "*", "*"}, "minute"},
		{[]string{"*", "24", "*", "*", "*"}, "hour"},
		{[]string{"*", "*", "32", "*", "*"}, "day of month"},
		{[]string{"*", "*", "*", "13", "*"}, "month"},
		{[]string{"*", "*", "*", "*", "8"}, "day of week"},
	}
	for _, tt := range tests {
		_, err := ParseSchedule(tt.fields)
		if err == nil {
			t.Errorf("ParseSchedule(%v): expected error, got none", tt.fields)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ParseSchedule(%v) error %q does not mention %q", tt.fields, err, tt.want)
		}
	}
}

func TestParseScheduleSundayAliases(t *testing.T) {
	s, err := ParseSchedule([]string{"0", "0", "*", "*", "7"})
	if err != nil {
		t.Fatal(err)
	}
	if s.dayOfWeek != bits(0) {
		t.Errorf("day-of-week 7 parsed as %b, want Sunday bit %b", s.dayOfWeek, bits(0))
	}
	s, err = ParseSchedule([]string{"0", "0", "*", "*", "0,7"})
	if err != nil {
		t.Fatal(err)
	}
	if s.dayOfWeek != bits(0) {
		t.Errorf("day-of-week 0,7 parsed as %b, want Sunday bit %b", s.dayOfWeek, bits(0))
	}
	s, err = ParseSchedule([]string{"0", "0", "*", "*", "5-7"})
	if err != nil {
		t.Fatal(err)
	}
	if s.dayOfWeek != bits(0, 5, 6) {
		t.Errorf("day-of-week 5-7 parsed as %b, want %b", s.dayOfWeek, bits(0, 5, 6))
	}
}

func TestParseScheduleRestrictedFlags(t *testing.T) {
	tests := []struct {
		dom, dow                   string
		wantDOMRestr, wantDOWRestr bool
	}{
		{"*", "*", false, false},
		{"1", "*", true, false},
		{"*", "1", false, true},
		{"1", "1", true, true},
	}
	for _, tt := range tests {
		s, err := ParseSchedule([]string{"*", "*", tt.dom, "*", tt.dow})
		if err != nil {
			t.Fatal(err)
		}
		if s.dayOfMonthRestricted != tt.wantDOMRestr || s.dayOfWeekRestricted != tt.wantDOWRestr {
			t.Errorf("dom=%q dow=%q: restricted flags = (%v, %v), want (%v, %v)",
				tt.dom, tt.dow, s.dayOfMonthRestricted, s.dayOfWeekRestricted, tt.wantDOMRestr, tt.wantDOWRestr)
		}
	}
}

func mustParse(t *testing.T, expr string) Schedule {
	t.Helper()
	s, err := ParseSchedule(strings.Fields(expr))
	if err != nil {
		t.Fatalf("ParseSchedule(%q): %v", expr, err)
	}
	return s
}

func at(year int, month time.Month, day, hour, min int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, time.Local)
}

func TestMatches(t *testing.T) {
	// Weekday anchors used below; fail loudly if they are wrong.
	anchors := []struct {
		day  time.Time
		want time.Weekday
	}{
		{at(2026, time.July, 5, 0, 0), time.Sunday},
		{at(2026, time.July, 6, 0, 0), time.Monday},
		{at(2026, time.July, 10, 0, 0), time.Friday},
		{at(2026, time.March, 13, 0, 0), time.Friday},
		{at(2026, time.April, 13, 0, 0), time.Monday},
	}
	for _, a := range anchors {
		if a.day.Weekday() != a.want {
			t.Fatalf("%s is a %s, expected %s", a.day.Format("2006-01-02"), a.day.Weekday(), a.want)
		}
	}

	tests := []struct {
		expr string
		time time.Time
		want bool
	}{
		{"* * * * *", at(2026, time.July, 6, 12, 34), true},
		{"30 5 * * *", at(2026, time.July, 6, 5, 30), true},
		{"30 5 * * *", at(2026, time.July, 6, 5, 31), false},
		{"30 5 * * *", at(2026, time.July, 6, 6, 30), false},
		{"*/15 * * * *", at(2026, time.July, 6, 8, 45), true},
		{"*/15 * * * *", at(2026, time.July, 6, 8, 46), false},
		{"0 9-17 * * *", at(2026, time.July, 6, 9, 0), true},
		{"0 9-17 * * *", at(2026, time.July, 6, 17, 0), true},
		{"0 9-17 * * *", at(2026, time.July, 6, 18, 0), false},
		{"59 23 31 12 *", at(2026, time.December, 31, 23, 59), true},
		{"0 0 1 1 *", at(2026, time.January, 1, 0, 0), true},
		{"0 0 1 1 *", at(2026, time.February, 1, 0, 0), false},
		{"0 0 * 7 *", at(2026, time.July, 6, 0, 0), true},
		{"0 0 * 7 *", at(2026, time.June, 6, 0, 0), false},

		// Day-of-week, including Sunday as both 0 and 7.
		{"0 0 * * 1", at(2026, time.July, 6, 0, 0), true},
		{"0 0 * * 2", at(2026, time.July, 6, 0, 0), false},
		{"0 0 * * 0", at(2026, time.July, 5, 0, 0), true},
		{"0 0 * * 7", at(2026, time.July, 5, 0, 0), true},

		// Only day-of-month restricted: day-of-week must not filter.
		{"0 0 13 * *", at(2026, time.April, 13, 0, 0), true},
		{"0 0 13 * *", at(2026, time.April, 14, 0, 0), false},

		// Both restricted: standard cron ORs the two day fields.
		{"0 0 13 * 5", at(2026, time.March, 13, 0, 0), true}, // Friday the 13th
		{"0 0 13 * 5", at(2026, time.April, 13, 0, 0), true}, // Monday the 13th
		{"0 0 13 * 5", at(2026, time.July, 10, 0, 0), true},  // Friday the 10th
		{"0 0 13 * 5", at(2026, time.July, 6, 0, 0), false},  // Monday the 6th

		// Lists and steps combined.
		{"0,30 12 * * *", at(2026, time.July, 6, 12, 30), true},
		{"0,30 12 * * *", at(2026, time.July, 6, 12, 15), false},
		{"0 0 */10 * *", at(2026, time.July, 11, 0, 0), true},
		{"0 0 */10 * *", at(2026, time.July, 12, 0, 0), false},
	}
	for _, tt := range tests {
		s := mustParse(t, tt.expr)
		if got := s.Matches(tt.time); got != tt.want {
			t.Errorf("%q.Matches(%s) = %v, want %v", tt.expr, tt.time.Format("2006-01-02 15:04 Mon"), got, tt.want)
		}
	}
}
