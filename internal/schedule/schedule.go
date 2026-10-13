// Package schedule parses comi schedules and computes run times.
package schedule

import (
	"fmt"
	"strings"
	"time"
)

// Schedule is either a fixed interval ("30m", "2h") or a list of daily
// times ("20:00", "09:00,18:00").
type Schedule struct {
	every time.Duration
	at    []int // minutes since midnight
}

func Parse(s string) (Schedule, error) {
	s = strings.TrimSpace(s)
	if d, err := time.ParseDuration(s); err == nil {
		if d < time.Minute {
			return Schedule{}, fmt.Errorf("interval %q is too short (minimum 1m)", s)
		}
		return Schedule{every: d}, nil
	}

	var sc Schedule
	for _, part := range strings.Split(s, ",") {
		t, err := time.Parse("15:04", strings.TrimSpace(part))
		if err != nil {
			return Schedule{}, fmt.Errorf("invalid schedule %q: use an interval (30m, 2h) or daily times (20:00 or 09:00,18:00)", s)
		}
		sc.at = append(sc.at, t.Hour()*60+t.Minute())
	}
	return sc, nil
}

// Next returns the first run time strictly after the given time.
func (s Schedule) Next(after time.Time) time.Time {
	after = after.In(time.Local)
	if s.every > 0 {
		return after.Add(s.every)
	}

	var next time.Time
	y, m, d := after.Date()
	for _, min := range s.at {
		t := time.Date(y, m, d, min/60, min%60, 0, 0, time.Local)
		if !t.After(after) {
			t = t.AddDate(0, 0, 1)
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	return next
}
