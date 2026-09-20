package ics

import (
	"fmt"
	"sort"
	"time"
)

// ruleAnchorTime is the time the RRULE engine enumerates from: DTSTART with
// its original location (TZID zone, UTC, or the deterministic floating zone).
func (e *expander) ruleAnchorTime() time.Time {
	return e.start.Time
}

// anchorDateTime wraps an iterator-generated time as a semantic DateTime,
// inheriting DTSTART's kind. The iterator works in the DTSTART location, so
// TZID/floating/UTC semantics are preserved end to end.
func (e *expander) anchorDateTime(t time.Time) DateTime {
	switch e.start.Kind {
	case TimeKindUTC:
		return DateTime{Time: t.UTC(), Kind: TimeKindUTC}
	case TimeKindTZID:
		return DateTime{Time: t.In(e.start.Time.Location()), Kind: TimeKindTZID, TZID: e.start.TZID}
	case TimeKindDate:
		return DateTime{Time: t, Kind: TimeKindDate, DateOnly: true}
	default:
		return DateTime{Time: t.In(e.floating), Kind: TimeKindFloating}
	}
}

// instanceEnd computes the occurrence end by applying the master event's
// duration (DTEND-DTSTART, or DURATION) to the occurrence. Wall-clock
// components are reconstructed so duration stays calendrical across DST,
// matching RFC 5545's duration semantics for date-time values.
func (e *expander) instanceEnd(start DateTime) DateTime {
	if !e.hasEnd {
		return DateTime{}
	}
	if e.start.Kind == TimeKindDate {
		// VALUE=DATE spans whole days; DTEND is exclusive, so the span is a
		// civil day difference unaffected by zone hours.
		days := civilDiffDays(e.start.Time, e.end.Time)
		end := time.Date(start.Time.Year(), start.Time.Month(), start.Time.Day(), 0, 0, 0, 0, start.Time.Location()).AddDate(0, 0, days)
		return DateTime{Time: end, Kind: TimeKindDate, DateOnly: true}
	}
	dur := e.end.Time.Sub(e.start.Time)
	y, m, d := start.Time.Date()
	// Prefer wall-clock component addition: add the duration's days at the
	// calendar level and the remainder as a duration so a 1-day event crossing
	// a DST change ends at the same local clock time.
	totalDays := int(dur.Hours() / 24)
	rem := dur - time.Duration(totalDays)*24*time.Hour
	base := time.Date(y, m, d+totalDays, start.Time.Hour(), start.Time.Minute(), start.Time.Second(), 0, start.Time.Location())
	end := base.Add(rem)
	return DateTime{Time: end, Kind: start.Kind, TZID: start.TZID}
}

// sortSources orders provenance deterministically: kind, then rule index, then
// raw value.
func sortSources(ss []Source) {
	sort.SliceStable(ss, func(i, j int) bool {
		if ss[i].Kind != ss[j].Kind {
			return sourceRank(ss[i].Kind) < sourceRank(ss[j].Kind)
		}
		if ss[i].RuleIndex != ss[j].RuleIndex {
			return ss[i].RuleIndex < ss[j].RuleIndex
		}
		return ss[i].Value < ss[j].Value
	})
}

func sourceRank(k SourceKind) int {
	switch k {
	case SourceStart:
		return 0
	case SourceRRule:
		return 1
	case SourceRDate:
		return 2
	case SourceOverride:
		return 3
	case SourceExclusion, SourceExRule:
		return 4
	}
	return 9
}

// sortInstances sorts ascending, anchored values by absolute instant and
// floating/date values by wall clock; ties break deterministically on kind.
func sortInstances(is []*Instance) {
	sort.SliceStable(is, func(i, j int) bool { return instanceLess(is[i].Start, is[j].Start) })
}

func instanceLess(a, b DateTime) bool {
	af := a.Kind == TimeKindFloating
	bf := b.Kind == TimeKindFloating
	if af != bf {
		return af
	}
	var at, bt time.Time
	if af {
		at, bt = a.Time, b.Time
	} else {
		at, bt = a.Time.UTC(), b.Time.UTC()
	}
	if !at.Equal(bt) {
		return at.Before(bt)
	}
	if a.Kind != b.Kind {
		return a.Kind.String() < b.Kind.String()
	}
	return a.TZID < b.TZID
}

func exclusionReasons(ss []Source) []Source {
	var out []Source
	for _, s := range ss {
		if s.Kind == SourceExclusion || s.Kind == SourceExRule {
			out = append(out, s)
		}
	}
	return out
}

func formatRaw(d DateTime) string {
	switch d.Kind {
	case TimeKindDate:
		return d.Time.Format(icalDateFormatLocal)
	case TimeKindUTC:
		return d.Time.UTC().Format(icalTimestampFormatUtc)
	default:
		return d.Time.Format(icalTimestampFormatLocal)
	}
}

// EventSeries is the expansion of all VEVENT components sharing one UID.
type EventSeries struct {
	// UID is the event unique identifier.
	UID string
	// Master is the recurring master; nil for orphan single events.
	Master *VEvent
	// Result is the expansion outcome for the series.
	Result *Result
}

// ExpandCalendar groups the calendar's VEVENTs by UID, pairs every recurring
// master with its RECURRENCE-ID overrides, and expands each series.
//
// Embedded VTIMEZONE components are compiled automatically, so TZID values
// defined inside the calendar work without host tzdata. Series are returned in
// ascending UID order so output never depends on component (map) order.
func ExpandCalendar(cal *Calendar, windowStart, windowEnd time.Time, maxInstances int, opts ...any) ([]EventSeries, error) {
	if cal == nil {
		return nil, fmt.Errorf("expand: nil calendar")
	}
	byTZID := map[string]*VTimezone{}
	for _, tz := range cal.Timezones() {
		if p := tz.GetProperty(ComponentPropertyTzid); p != nil {
			byTZID[p.Value] = tz
		}
	}
	vtzResolver := TimezoneResolver(func(tzid string) *time.Location {
		if vtz, ok := byTZID[tzid]; ok {
			if loc, err := VTimezoneLocation(vtz); err == nil {
				return loc
			}
		}
		return nil
	})

	type group struct {
		master    *VEvent
		overrides []*VEvent
	}
	groups := map[string]*group{}
	var uids []string
	ensure := func(uid string) *group {
		g, ok := groups[uid]
		if !ok {
			g = &group{}
			groups[uid] = g
			uids = append(uids, uid)
		}
		return g
	}
	for _, ev := range cal.Events() {
		uid := ev.Id()
		g := ensure(uid)
		if ev.GetProperty(ComponentPropertyRecurrenceId) != nil {
			g.overrides = append(g.overrides, ev)
			continue
		}
		if g.master == nil {
			g.master = ev
		} else if ev.GetProperty(ComponentPropertyRrule) != nil || ev.GetProperty(ComponentPropertyRdate) != nil {
			g.master = ev
		}
	}

	allOpts := append([]any{WithTimezoneResolvers(vtzResolver)}, opts...)
	sort.Strings(uids)
	series := make([]EventSeries, 0, len(uids))
	for _, uid := range uids {
		g := groups[uid]
		if g.master == nil {
			continue
		}
		res, err := ExpandEvent(g.master, windowStart, windowEnd, maxInstances, g.overrides, allOpts...)
		if err != nil && res == nil {
			return nil, fmt.Errorf("expand %q: %w", uid, err)
		}
		series = append(series, EventSeries{UID: uid, Master: g.master, Result: res})
	}
	return series, nil
}

// civilDiffDays returns the whole-day difference b-a using a zone-independent
// day ordinal.
func civilDiffDays(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	da := time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC)
	db := time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC)
	return int(db.Sub(da).Hours() / 24)
}
