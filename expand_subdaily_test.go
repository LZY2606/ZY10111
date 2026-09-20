package ics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExpandSubDailyRules verifies HOURLY/MINUTELY with BY rules and sub-day
// BYSETPOS. These are validated directly against the production iterator
// (the finite day-scanning oracle deliberately excludes sub-day frequencies).
func TestExpandSubDailyRules(t *testing.T) {
	loc := time.UTC
	type tc struct {
		name  string
		start string
		rule  string
		want  []string
	}
	cases := []tc{
		{
			name:  "hourly count",
			start: "20240101T090000",
			rule:  "FREQ=HOURLY;COUNT=4",
			want:  []string{"2024-01-01T09:00:00Z", "2024-01-01T10:00:00Z", "2024-01-01T11:00:00Z", "2024-01-01T12:00:00Z"},
		},
		{
			name:  "minutely interval",
			start: "20240101T090000",
			rule:  "FREQ=MINUTELY;INTERVAL=30;COUNT=3",
			want:  []string{"2024-01-01T09:00:00Z", "2024-01-01T09:30:00Z", "2024-01-01T10:00:00Z"},
		},
		{
			name:  "daily with byhour/byminute expansion",
			start: "20240101T090000",
			rule:  "FREQ=DAILY;BYHOUR=9,12;BYMINUTE=0,30;COUNT=6",
			want: []string{
				"2024-01-01T09:00:00Z", "2024-01-01T09:30:00Z", "2024-01-01T12:00:00Z",
				"2024-01-01T12:30:00Z", "2024-01-02T09:00:00Z", "2024-01-02T09:30:00Z",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, err := time.ParseInLocation("20060102T150405", c.start, loc)
			require.NoError(t, err)
			r, err := ParseRecurrenceRule(c.rule)
			require.NoError(t, err)
			it, err := newRuleIterator(r, start)
			require.NoError(t, err)
			var got []string
			for {
				v, ok, err := it.Next()
				require.NoError(t, err)
				if !ok {
					break
				}
				got = append(got, v.UTC().Format("2006-01-02T15:04:05Z"))
				if len(got) > len(c.want) {
					t.Fatalf("overshot: %v", got)
				}
			}
			assert.Equal(t, c.want, got)
		})
	}
}

// TestExpandInvalidRules verifies malformed rules are rejected rather than
// producing surprising output.
func TestExpandInvalidRules(t *testing.T) {
	start := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
	bad := []string{
		"FREQ=DAILY;INTERVAL=0",
		"FREQ=WEEKLY;BYWEEKNO=2",
		"FREQ=YEARLY;BYWEEKNO=20;BYMONTH=3",
		"FREQ=DAILY;BYDAY=2MO",
		"FREQ=DAILY;BYHOUR=24",
	}
	for _, br := range bad {
		r, err := ParseRecurrenceRule(br)
		require.NoError(t, err, br)
		_, err = newRuleIterator(r, start)
		assert.ErrorIs(t, err, ErrInvalidRecurrenceRule, br)
	}
}
