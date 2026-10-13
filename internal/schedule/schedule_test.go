package schedule

import (
	"reflect"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	valid := map[string]Schedule{
		"30m":          {every: 30 * time.Minute},
		" 2h ":         {every: 2 * time.Hour},
		"1h30m":        {every: 90 * time.Minute},
		"1m":           {every: time.Minute},
		"20:00":        {at: []int{20 * 60}},
		"09:00, 18:30": {at: []int{9 * 60, 18*60 + 30}},
	}
	for in, want := range valid {
		got, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): unexpected error: %v", in, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Parse(%q) = %+v, want %+v", in, got, want)
		}
	}

	for _, in := range []string{"", "10s", "-5m", "abc", "9", "25:00", "09:00,", "20:00,abc"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q): expected an error", in)
		}
	}
}

func TestScheduleNext(t *testing.T) {
	at := func(day, hour, min int) time.Time {
		return time.Date(2026, time.January, day, hour, min, 0, 0, time.Local)
	}
	tests := []struct {
		schedule string
		after    time.Time
		want     time.Time
	}{
		{"30m", at(15, 10, 0), at(15, 10, 30)},
		{"2h", at(15, 23, 0), at(16, 1, 0)},
		{"20:00", at(15, 10, 0), at(15, 20, 0)},
		{"09:00", at(15, 10, 0), at(16, 9, 0)},
		{"10:00", at(15, 10, 0), at(16, 10, 0)}, // strictly after
		{"18:00,09:00", at(15, 10, 0), at(15, 18, 0)},
		{"08:00,09:00", at(15, 10, 0), at(16, 8, 0)},
		{"08:00", at(31, 22, 0), time.Date(2026, time.February, 1, 8, 0, 0, 0, time.Local)},
	}
	for _, tt := range tests {
		sc, err := Parse(tt.schedule)
		if err != nil {
			t.Fatal(err)
		}
		if got := sc.Next(tt.after); !got.Equal(tt.want) {
			t.Errorf("%q after %s: got %s, want %s", tt.schedule, tt.after, got, tt.want)
		}
	}
}
