package ics

import (
	"fmt"
	"testing"
	"time"
)

// TestOracleVsIterator cross-checks the production ruleIterator against the
// independent day-scanning oracle across a matrix of finite rules. The two
// implementations share only the RRULE string parser.
func TestOracleVsIterator(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	type anchor struct {
		name  string
		start time.Time
	}
	anchors := []anchor{
		{"jan-mon-ny", time.Date(2023, 1, 2, 9, 0, 0, 0, loc)},
		{"jan31-ny", time.Date(2023, 1, 31, 9, 30, 0, 0, loc)},
		{"feb29-leap-ny", time.Date(2024, 2, 29, 9, 0, 0, 0, loc)},
		{"dst-march-ny", time.Date(2024, 3, 6, 2, 30, 0, 0, loc)},
		{"wed-utc", time.Date(2023, 6, 7, 12, 0, 0, 0, time.UTC)},
		{"nov-ny", time.Date(2023, 11, 1, 9, 0, 0, 0, loc)},
	}
	rules := []string{
		"FREQ=DAILY;COUNT=40",
		"FREQ=DAILY;INTERVAL=3;COUNT=20",
		"FREQ=DAILY;INTERVAL=2;UNTIL=20240201T000000Z",
		"FREQ=WEEKLY;COUNT=20",
		"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR;COUNT=24",
		"FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20230901T000000Z",
		"FREQ=WEEKLY;WKST=SU;BYDAY=SU,SA;COUNT=15",
		"FREQ=MONTHLY;COUNT=18",
		"FREQ=MONTHLY;INTERVAL=2;COUNT=12",
		"FREQ=MONTHLY;BYMONTHDAY=1,15;COUNT=20",
		"FREQ=MONTHLY;BYMONTHDAY=-1;COUNT=14",
		"FREQ=MONTHLY;BYMONTHDAY=31;COUNT=10",
		"FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=12",
		"FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=1;COUNT=12",
		"FREQ=MONTHLY;BYDAY=2MO;COUNT=10",
		"FREQ=MONTHLY;BYDAY=-1SU;COUNT=10",
		"FREQ=MONTHLY;BYDAY=TH;BYSETPOS=4;COUNT=8",
		"FREQ=MONTHLY;UNTIL=20241231T000000Z",
		"FREQ=YEARLY;COUNT=5",
		"FREQ=YEARLY;BYMONTH=6,7;COUNT=6",
		"FREQ=YEARLY;BYMONTH=11;BYDAY=TH;BYSETPOS=4;COUNT=6",
		"FREQ=YEARLY;BYMONTH=2;BYDAY=-1SU;COUNT=5",
		"FREQ=YEARLY;BYMONTH=10;BYMONTHDAY=31;COUNT=5",
		"FREQ=YEARLY;BYDAY=20MO;COUNT=5",
		"FREQ=YEARLY;BYDAY=MO;BYSETPOS=1;COUNT=4",
		"FREQ=DAILY;BYDAY=MO,WE,FR;COUNT=15",
		"FREQ=MONTHLY;BYMONTH=2;BYDAY=MO;COUNT=6",
		"FREQ=MONTHLY;BYMONTHDAY=15;BYDAY=MO;COUNT=6",
	}
	for _, a := range anchors {
		for _, rs := range rules {
			name := fmt.Sprintf("%s/%s", a.name, rs)
			t.Run(name, func(t *testing.T) {
				r, perr := ParseRecurrenceRule(rs)
				if perr != nil {
					t.Fatal(perr)
				}
				want := oracleExpand(t, r, a.start)
				it, ierr := newRuleIterator(r, a.start)
				if ierr != nil {
					t.Fatal(ierr)
				}
				var got []time.Time
				for {
					v, ok, err := it.Next()
					if err != nil {
						t.Fatal(err)
					}
					if !ok {
						break
					}
					got = append(got, v)
					if len(got) > len(want)+5 {
						t.Fatalf("iterator overshot: got %d want %d", len(got), len(want))
					}
				}
				if len(got) != len(want) {
					t.Fatalf("count mismatch: got %d want %d\ngot=%v\nwant=%v", len(got), len(want), fmtTimes(got), fmtTimes(want))
				}
				for i := range want {
					if !got[i].Equal(want[i]) {
						t.Fatalf("at %d: got %s want %s\ngot=%v\nwant=%v", i, got[i].UTC().Format(time.RFC3339), want[i].UTC().Format(time.RFC3339), fmtTimes(got), fmtTimes(want))
					}
				}
			})
		}
	}
}

func fmtTimes(ts []time.Time) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.In(time.UTC).Format("2006-01-02T15:04:05Z")
	}
	return out
}
