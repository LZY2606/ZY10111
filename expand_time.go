package ics

import (
	"strconv"
	"strings"
	"time"
)

// TimeKind classifies how a recurrence date-time value was specified in the
// iCalendar source. The distinction matters because the three kinds are not
// freely interchangeable: matching, windowing and serialization all depend on
// it (RFC 5545 section 3.3.5).
type TimeKind int

const (
	// TimeKindUTC is a value expressed in UTC (trailing "Z").
	TimeKindUTC TimeKind = iota
	// TimeKindTZID is a local wall-clock value tagged with a TZID parameter.
	TimeKindTZID
	// TimeKindFloating is a local wall-clock value with no TZID and no "Z".
	// Floating times intentionally carry no absolute instant; they must never
	// be interpreted using the host machine's local zone.
	TimeKindFloating
	// TimeKindDate is a date-only value (VALUE=DATE, e.g. 20240131).
	TimeKindDate
)

func (k TimeKind) String() string {
	switch k {
	case TimeKindUTC:
		return "UTC"
	case TimeKindTZID:
		return "TZID"
	case TimeKindFloating:
		return "FLOATING"
	case TimeKindDate:
		return "DATE"
	}
	return "UNKNOWN"
}

// DateTime is a recurrence date value that retains its original time
// semantics instead of collapsing everything onto a time.Time in some zone.
//
// For anchored kinds (TimeKindUTC, TimeKindTZID and TimeKindDate interpreted
// in the event's zone) Instant is the absolute UTC instant and Location the
// zone used to express the wall clock. For TimeKindFloating, Location is a
// zone local to the expansion call and Instant carries no portable meaning.
type DateTime struct {
	// Time is the parsed value. For TimeKindFloating its location is a
	// deterministic synthetic zone supplied by the expansion options, never
	// time.Local.
	Time time.Time
	// Kind records how the value was specified in the source.
	Kind TimeKind
	// TZID is the original TZID parameter for TimeKindTZID; empty otherwise.
	TZID string
	// DateOnly mirrors ValueDataTypeDate properties.
	DateOnly bool
}

// Instant returns the absolute instant represented by the value. Callers must
// not rely on it for floating values; use SameOccurrence to compare.
func (d DateTime) Instant() time.Time { return d.Time.UTC() }

// WallClock reports the local year, month, day, hour, minute and second exactly
// as written in the source, without forcing a zone conversion.
func (d DateTime) WallClock() (year int, month time.Month, day, hour, min, sec int) {
	year, month, day = d.Time.Date()
	return year, month, day, d.Time.Hour(), d.Time.Minute(), d.Time.Second()
}

// SameOccurrence reports whether two values denote the same recurrence
// occurrence for the purposes of EXDATE/RDATE/RECURRENCE-ID matching and
// duplicate merging.
//
// Matching follows RFC 5545 section 3.8.5.1: values specified in the same
// time type match on their wall-clock components (UTC values match on their
// UTC wall clock, TZID values with the same effective zone on local wall
// clock, floating values only against floating values, dates against dates).
// Two anchored values of differing kinds additionally match when their
// absolute instants coincide, which is what calendars in practice rely on
// when an EXDATE is written in UTC against a TZID rule.
func SameOccurrence(a, b DateTime) bool {
	if a.Kind != b.Kind {
		if a.Kind == TimeKindFloating || b.Kind == TimeKindFloating {
			return false
		}
		if a.Kind == TimeKindDate || b.Kind == TimeKindDate {
			return false
		}
		return a.Time.Equal(b.Time)
	}
	switch a.Kind {
	case TimeKindFloating, TimeKindDate:
		return sameWallClock(a.Time, b.Time)
	case TimeKindTZID:
		if a.Time.Location().String() == b.Time.Location().String() {
			return sameWallClock(a.Time, b.Time)
		}
		return a.Time.Equal(b.Time)
	default:
		return a.Time.Equal(b.Time)
	}
}

func sameWallClock(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd &&
		a.Hour() == b.Hour() && a.Minute() == b.Minute() && a.Second() == b.Second()
}

// occurrenceKey returns a deterministic identity used for map-based merging.
// SameOccurrence-equivalent values must produce the same key, and unrelated
// values should produce different keys; floating values are keyed by wall
// clock, anchored values by their absolute instant.
func occurrenceKey(d DateTime) string {
	if d.Kind == TimeKindFloating || d.Kind == TimeKindDate {
		y, m, day, h, mi, s := d.WallClock()
		kind := "F"
		if d.Kind == TimeKindDate {
			kind = "D"
		}
		return itoaKey(kind, y, int(m), day, h, mi, s)
	}
	u := d.Time.UTC()
	return "T:" + u.Format("20060102T150405")
}

func itoaKey(prefix string, nums ...int) string {
	parts := make([]string, 1, len(nums)+1)
	parts[0] = prefix
	for _, n := range nums {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ":")
}
