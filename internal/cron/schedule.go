package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Schedule struct {
	minute, hour, dayOfMonth, month, dayOfWeek uint64
	dayOfMonthRestricted, dayOfWeekRestricted  bool
}

// Standard three-letter cron names for the month and day-of-week fields.
var monthAliases = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var weekdayAliases = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

func ParseSchedule(fields []string) (Schedule, error) {
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("expected 5 schedule fields, got %d", len(fields))
	}
	var s Schedule
	var err error
	if s.minute, err = parseField(fields[0], 0, 59, nil); err != nil {
		return Schedule{}, fmt.Errorf("minute: %w", err)
	}
	if s.hour, err = parseField(fields[1], 0, 23, nil); err != nil {
		return Schedule{}, fmt.Errorf("hour: %w", err)
	}
	if s.dayOfMonth, err = parseField(fields[2], 1, 31, nil); err != nil {
		return Schedule{}, fmt.Errorf("day of month: %w", err)
	}
	if s.month, err = parseField(fields[3], 1, 12, monthAliases); err != nil {
		return Schedule{}, fmt.Errorf("month: %w", err)
	}
	if s.dayOfWeek, err = parseField(fields[4], 0, 7, weekdayAliases); err != nil {
		return Schedule{}, fmt.Errorf("day of week: %w", err)
	}
	if s.dayOfWeek&(1<<7) != 0 {
		s.dayOfWeek = (s.dayOfWeek &^ (1 << 7)) | 1
	}
	s.dayOfMonthRestricted = fields[2] != "*"
	s.dayOfWeekRestricted = fields[4] != "*"
	return s, nil
}

func parseField(spec string, lo, hi int, aliases map[string]int) (uint64, error) {
	var mask uint64
	for _, part := range strings.Split(spec, ",") {
		rangeSpec, stepSpec, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepSpec)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("invalid step %q", part)
			}
			step = n
		}
		start, end := lo, hi
		if rangeSpec != "*" {
			startSpec, endSpec, hasRange := strings.Cut(rangeSpec, "-")
			n, err := parseFieldValue(startSpec, aliases)
			if err != nil {
				return 0, fmt.Errorf("invalid value %q", part)
			}
			start = n
			if hasRange {
				n, err := parseFieldValue(endSpec, aliases)
				if err != nil {
					return 0, fmt.Errorf("invalid range %q", part)
				}
				end = n
			} else if hasStep {
				end = hi
			} else {
				end = start
			}
		}
		if start < lo || end > hi || start > end {
			return 0, fmt.Errorf("value out of range %d-%d in %q", lo, hi, part)
		}
		for v := start; v <= end; v += step {
			mask |= 1 << v
		}
	}
	return mask, nil
}

// parseFieldValue resolves a value via the alias table (case-insensitive)
// before falling back to a plain number.
func parseFieldValue(s string, aliases map[string]int) (int, error) {
	if aliases != nil {
		if n, ok := aliases[strings.ToUpper(s)]; ok {
			return n, nil
		}
	}
	return strconv.Atoi(s)
}

func (s Schedule) Matches(t time.Time) bool {
	if s.minute&(1<<t.Minute()) == 0 ||
		s.hour&(1<<t.Hour()) == 0 ||
		s.month&(1<<int(t.Month())) == 0 {
		return false
	}
	dayOfMonthOK := s.dayOfMonth&(1<<t.Day()) != 0
	dayOfWeekOK := s.dayOfWeek&(1<<int(t.Weekday())) != 0
	if s.dayOfMonthRestricted && s.dayOfWeekRestricted {
		return dayOfMonthOK || dayOfWeekOK
	}
	return dayOfMonthOK && dayOfWeekOK
}
