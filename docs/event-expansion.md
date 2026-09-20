# Expanding recurring VEVENTs into provenanced instances

The core library parses and serializes RFC 5545 components, including `RRULE`,
`RDATE`, `EXDATE`, `EXRULE` and `RECURRENCE-ID`, but reading those properties
still leaves every caller to reimplement recurrence expansion. The opt-in
expansion API described here turns a master `VEVENT` into a stable, ordered list
of concrete instances inside a time window **and explains why each instance
exists**.

The API is additive: existing parse/serialize/getter interfaces are unchanged.

## Why instances carry evidence

An occurrence can exist for more than one reason. Two `RRULE` properties may
generate the same instant; `DTSTART` can itself be the first `RRULE` member; an
`RDATE` can coincide with a generated occurrence. Conversely, an occurrence can
be present in the raw recurrence set but removed by an `EXDATE`, an `EXRULE`, or
a `STATUS:CANCELLED` `RECURRENCE-ID` override. Returning a bare `[]time.Time`
loses all of that information.

Every `Instance` therefore carries:

- `Start` / `End` as `TimeValue`, which preserves the source semantics
  (`UTC`, `TZID`, floating, or `VALUE=DATE`) together with the absolute instant;
- `Sources`, an ordered, de-duplicated list of `InstanceSource` evidence
  (`DTSTART`, `RRULE` with the rule index and in-rule sequence, `RDATE`);
- `Override`, present when a `RECURRENCE-ID` VEVENT replaces the instance.

Excluded occurrences never appear in `Instances`. Pass `WithDiagnostics()` to
also receive `Excluded` (with the reason) and `OrphanOverrides`; those slices
stay empty in normal iteration.

## Window, cap and termination

`Window` is **half-open** (`[From, To)`) by default; set `IncludeTo` for a
closed upper bound. A zero bound is unbounded on that side.

`COUNT` counts occurrences starting at `DTSTART` even when they fall before the
window; only the surviving in-window instances are returned. `UNTIL` is
inclusive and evaluated against the absolute instant; a date-only `UNTIL` covers
the whole day.

Every expansion is bounded by `WithMaxInstances` (default
`DefaultMaxInstances = 1000`). When the cap is hit while more in-window
instances exist, `ExpansionResult.Truncated` is `TruncatedMaxInstances` rather
than the result pretending the rule ended naturally. A natural `COUNT`/`UNTIL`
end yields `NotTruncated`.

## Time semantics

- A `TZID` value is interpreted in that location and keeps its `TZID`;
- a `Z` value stays UTC;
- a floating value is interpreted deterministically through
  `WithFloatingLocation` (UTC by default) — never the host `time.Local`;
- a `VALUE=DATE` value stays a date.

Wall clocks, `TZID`, date-only flags and UTC instants are all retained on
`TimeValue`, so rendering or round-tripping does not require guessing. Unknown
time zones produce a typed error unless resolved through
`WithExpandTimezoneMapper`.

## Full example: daily meeting crossing a DST transition with an override

The meeting below starts at 09:00 America/New_York and recurs daily across the
March 10, 2024 spring-forward. One occurrence is rescheduled by a
`RECURRENCE-ID` override, and another is removed with `EXDATE`.

```go
package main

import (
	"fmt"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
)

const data = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:example\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:standup@example.com\r\n" +
	"DTSTART;TZID=America/New_York:20240307T090000\r\n" +
	"DTEND;TZID=America/New_York:20240307T093000\r\n" +
	"RRULE:FREQ=DAILY;COUNT=6\r\n" +
	"EXDATE;TZID=America/New_York:20240308T090000\r\n" +
	"SUMMARY:Daily standup\r\n" +
	"END:VEVENT\r\n" +
	// The March 11 occurrence is moved to 16:00 local.
	"BEGIN:VEVENT\r\n" +
	"UID:standup@example.com\r\n" +
	"RECURRENCE-ID;TZID=America/New_York:20240311T090000\r\n" +
	"DTSTART;TZID=America/New_York:20240311T160000\r\n" +
	"DTEND;TZID=America/New_York:20240311T163000\r\n" +
	"SUMMARY:Standup moved to afternoon\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func main() {
	cal, err := ics.ParseCalendar(strings.NewReader(data))
	if err != nil {
		panic(err)
	}

	results, err := ics.ExpandCalendar(cal,
		ics.WithWindow(ics.Window{
			From: time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC),
		}),
		ics.WithMaxInstances(100),
		ics.WithDiagnostics(),
	)
	if err != nil {
		panic(err)
	}

	for _, group := range results {
		for _, in := range group.Instances {
			var reasons []string
			for _, s := range in.Sources {
				if s.RuleIndex >= 0 {
					reasons = append(reasons, fmt.Sprintf("%s#%d", s.Kind, s.RuleIndex))
				} else {
					reasons = append(reasons, string(s.Kind))
				}
			}
			note := strings.Join(reasons, ",")
			if in.Override != nil {
				note += " +RECURRENCE-ID"
			}
			fmt.Printf("%s -> %s  [%s]  %s\n",
				in.Start.Time.UTC().Format(time.RFC3339),
				in.End.Time.UTC().Format(time.RFC3339),
				in.Start.TZID,
				note,
			)
		}
		for _, ex := range group.Excluded {
			fmt.Printf("(removed %s by %s)\n",
				ex.Start.Time.UTC().Format(time.RFC3339), ex.Reason)
		}
	}
}
```

Output (the two offsets across the DST jump are `-05:00` then `-04:00`):

```
2024-03-07T14:00:00Z -> 2024-03-07T14:30:00Z  [America/New_York]  DTSTART,RRULE#0
2024-03-09T14:00:00Z -> 2024-03-09T14:30:00Z  [America/New_York]  RRULE#0
2024-03-10T13:00:00Z -> 2024-03-10T13:30:00Z  [America/New_York]  RRULE#0
2024-03-11T20:00:00Z -> 2024-03-11T20:30:00Z  [America/New_York]  RRULE#0 +RECURRENCE-ID
2024-03-12T13:00:00Z -> 2024-03-12T13:30:00Z  [America/New_York]  RRULE#0
(removed 2024-03-08T14:00:00Z by EXDATE)
```

## Serialization round trip

Expansion operates on parsed components without mutating them. Serializing the
calendar again and re-parsing, then expanding with the same window, yields the
same instants, source categories and override relations (covered by
`TestExpandSerializationRoundTrip`). Results are independent of Go map
iteration order and of the host machine's default time zone.

## API surface

- `ExpandEvent(master *VEvent, opts ...ExpandOption) (*ExpansionResult, error)`
- `ExpandCalendar(cal *Calendar, opts ...ExpandOption) ([]*ExpansionResult, error)`
- Options: `WithWindow`, `WithMaxInstances`, `WithDiagnostics`,
  `WithExpandTimezoneMapper`, `WithFloatingLocation`,
  `WithExRulesIncluded`, `WithOverrides`.
- Types: `Window`, `Instance`, `TimeValue`, `InstanceSource`,
  `OverrideInfo`, `ExcludedInstance`, `ExpansionResult`.
