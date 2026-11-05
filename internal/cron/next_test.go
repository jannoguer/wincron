package cron

import (
	"strings"
	"testing"
	"time"
)

func mustSchedule(t *testing.T, spec string) Schedule {
	t.Helper()
	s, err := ParseSchedule(strings.Fields(spec))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNext(t *testing.T) {
	ny := newYork(t)
	tests := []struct {
		name string
		spec string
		from time.Time
		want time.Time
	}{
		{"later today", "30 10 * * *", at(2026, time.July, 6, 10, 29), at(2026, time.July, 6, 10, 30)},
		{"strictly after", "30 10 * * *", at(2026, time.July, 6, 10, 30), at(2026, time.July, 7, 10, 30)},
		{"seconds are ignored", "30 10 * * *", time.Date(2026, time.July, 6, 10, 29, 45, 0, time.Local), at(2026, time.July, 6, 10, 30)},
		{"month without the day", "0 0 31 * *", at(2026, time.January, 31, 0, 0), at(2026, time.March, 31, 0, 0)},
		{"year rollover", "0 0 1 1 *", at(2026, time.July, 6, 0, 0), at(2027, time.January, 1, 0, 0)},
		{"leap day", "0 0 29 2 *", at(2026, time.March, 1, 0, 0), at(2028, time.February, 29, 0, 0)},
		{"day of month or day of week", "0 0 13 * 5", at(2026, time.July, 10, 0, 0), at(2026, time.July, 13, 0, 0)},
		{"both restricted picks the earlier", "0 0 13 * 5", at(2026, time.July, 1, 0, 0), at(2026, time.July, 3, 0, 0)},
		{"unrestricted day of month ANDs", "0 0 */2 * 1", at(2026, time.July, 6, 0, 0), at(2026, time.July, 13, 0, 0)},
		{"skipped DST hour runs at the jump", "30 2 * * *", time.Date(2026, time.March, 8, 0, 0, 0, 0, ny), time.Date(2026, time.March, 8, 3, 0, 0, 0, ny)},
		{"day before the DST jump", "30 2 * * *", time.Date(2026, time.March, 7, 0, 0, 0, 0, ny), time.Date(2026, time.March, 7, 2, 30, 0, 0, ny)},
		{"repeated hour reports the first pass", "30 1 * * *", time.Date(2026, time.November, 1, 0, 0, 0, 0, ny), time.Date(2026, time.November, 1, 5, 30, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := mustSchedule(t, tt.spec).Next(tt.from)
			if !ok || !got.Equal(tt.want) {
				t.Errorf("Next = (%s, %v), want %s", got, ok, tt.want)
			}
		})
	}
}

func TestPrev(t *testing.T) {
	tests := []struct {
		name string
		spec string
		from time.Time
		want time.Time
	}{
		{"earlier today", "30 10 * * *", at(2026, time.July, 6, 10, 31), at(2026, time.July, 6, 10, 30)},
		{"inclusive", "30 10 * * *", time.Date(2026, time.July, 6, 10, 30, 20, 0, time.Local), at(2026, time.July, 6, 10, 30)},
		{"yesterday", "30 10 * * *", at(2026, time.July, 6, 10, 29), at(2026, time.July, 5, 10, 30)},
		{"month without the day", "0 0 31 * *", at(2026, time.March, 15, 0, 0), at(2026, time.January, 31, 0, 0)},
		{"year rollover", "0 0 1 1 *", at(2026, time.July, 6, 0, 0), at(2026, time.January, 1, 0, 0)},
		{"day of month or day of week", "0 0 13 * 5", at(2026, time.July, 12, 0, 0), at(2026, time.July, 10, 0, 0)},
		{"hour list", "15 8,20 * * *", at(2026, time.July, 6, 19, 0), at(2026, time.July, 6, 8, 15)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := mustSchedule(t, tt.spec).Prev(tt.from)
			if !ok || !got.Equal(tt.want) {
				t.Errorf("Prev = (%s, %v), want %s", got, ok, tt.want)
			}
		})
	}
}

func TestNextAndPrevNever(t *testing.T) {
	s := mustSchedule(t, "0 0 30 2 *")
	if got, ok := s.Next(at(2026, time.July, 6, 0, 0)); ok {
		t.Errorf("Next of 30 Feb = %s, want never", got)
	}
	if got, ok := s.Prev(at(2026, time.July, 6, 0, 0)); ok {
		t.Errorf("Prev of 30 Feb = %s, want never", got)
	}
}

func TestNextKeepsLocation(t *testing.T) {
	ny := newYork(t)
	got, ok := mustSchedule(t, "0 12 * * *").Next(time.Date(2026, time.July, 6, 0, 0, 0, 0, ny))
	if !ok || got.Location() != ny {
		t.Errorf("Next = (%s in %v, %v), want a time in New York", got, got.Location(), ok)
	}
}
