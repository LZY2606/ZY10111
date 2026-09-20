package ics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// sourcePresent reports whether an equivalent source already exists.
func sourcePresent(srcs []InstanceSource, s InstanceSource) bool {
	for _, existing := range srcs {
		if existing == s {
			return true
		}
	}
	return false
}

// sortInstances orders instances by start instant with a deterministic
// semantic tie breaker.
func sortInstances(in []Instance) {
	sort.SliceStable(in, func(i, j int) bool {
		a, b := in[i].Start, in[j].Start
		if !a.Time.Equal(b.Time) {
			return a.Time.Before(b.Time)
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.TZID != b.TZID {
			return a.TZID < b.TZID
		}
		_, ao := a.Time.Zone()
		_, bo := b.Time.Zone()
		return ao < bo
	})
}

// exclusionMatches implements EXDATE matching semantics.
func exclusionMatches(ex, inst TimeValue) bool {
	if ex.DateOnly != inst.DateOnly {
		return false
	}
	switch ex.Kind {
	case TimeKindFloating:
		return inst.Kind == TimeKindFloating && ex.Wall.Equal(inst.Wall)
	case TimeKindDate:
		return ex.Time.Equal(inst.Time)
	default: // UTC or TZID
		return (inst.Kind == TimeKindUTC || inst.Kind == TimeKindTZID) && ex.Time.Equal(inst.Time)
	}
}

// recIDMatches implements RECURRENCE-ID matching semantics: same rules as
// EXDATE, plus exact TZID identity is preferred when both carry one.
func recIDMatches(rec, inst TimeValue) bool {
	if rec.DateOnly != inst.DateOnly {
		return false
	}
	if rec.Kind == TimeKindTZID && inst.Kind == TimeKindTZID {
		return rec.TZID == inst.TZID && rec.Wall.Equal(inst.Wall)
	}
	return exclusionMatches(rec, inst)
}

// buildOverride parses one RECURRENCE-ID VEVENT into an OverrideInfo.
func (cfg *expansionConfig) buildOverride(event *VEvent) (OverrideInfo, error) {
	if event == nil {
		return OverrideInfo{}, fmt.Errorf("override event is nil")
	}
	recTV, ok, err := cfg.propTime(event, ComponentPropertyRecurrenceId)
	if err != nil {
		return OverrideInfo{}, err
	}
	if !ok {
		return OverrideInfo{}, fmt.Errorf("override event is missing RECURRENCE-ID")
	}
	rng := OverrideRangeSingle
	if p := event.GetProperty(ComponentPropertyRecurrenceId); p != nil {
		if vals, ok := p.ICalParameters[string(ParameterRange)]; ok && len(vals) == 1 {
			switch OverrideRange(vals[0]) {
			case OverrideRangeThisAndFuture, OverrideRangeThisAndPrior:
				rng = OverrideRange(vals[0])
			default:
				return OverrideInfo{}, fmt.Errorf("unsupported RECURRENCE-ID RANGE %q", vals[0])
			}
		}
	}
	cancelled := false
	if p := event.GetProperty(ComponentPropertyStatus); p != nil && strings.EqualFold(p.Value, string(ObjectStatusCancelled)) {
		cancelled = true
	}
	return OverrideInfo{
		Event:        event,
		RecurrenceID: recTV,
		Range:        rng,
		Cancelled:    cancelled,
	}, nil
}

// findOverride returns the first override replacing the given instance slot.
func findOverride(overrides []OverrideInfo, inst TimeValue) (int, OverrideInfo) {
	for i, ov := range overrides {
		if ov.Range != OverrideRangeSingle {
			continue
		}
		if recIDMatches(ov.RecurrenceID, inst) {
			return i, ov
		}
	}
	return -1, OverrideInfo{}
}

// cancelledRangeIndex returns a non-cancelled=false RANGE override that covers
// the instance. Only STATUS:CANCELLED range overrides are handled here; the
// divergent-series semantics of non-cancelled RANGE overrides are not expanded.
func cancelledRangeIndex(overrides []OverrideInfo, inst TimeValue) int {
	for i, ov := range overrides {
		if !ov.Cancelled || ov.Range == OverrideRangeSingle {
			continue
		}
		if recIDMatches(ov.RecurrenceID, inst) {
			return i
		}
		switch ov.Range {
		case OverrideRangeThisAndFuture:
			if inst.Time.After(ov.RecurrenceID.Time) {
				return i
			}
		case OverrideRangeThisAndPrior:
			if inst.Time.Before(ov.RecurrenceID.Time) {
				return i
			}
		}
	}
	return -1
}

// eventDuration returns the signed span from DTSTART to DTEND, or the DURATION
// property value, for a VEVENT.
func (cfg *expansionConfig) eventDuration(event *VEvent, dtstart TimeValue) (time.Duration, bool, error) {
	if endTV, ok, err := cfg.propTime(event, ComponentPropertyDtEnd); err != nil {
		return 0, false, expansionError("dtend", err)
	} else if ok {
		return endTV.Time.Sub(dtstart.Time), true, nil
	}
	if p := event.GetProperty(ComponentPropertyDuration); p != nil {
		d, err := parseISODuration(p.Value)
		if err != nil {
			return 0, false, expansionError("duration", err)
		}
		return d, true, nil
	}
	return 0, false, nil
}

// shiftTimeValue preserves the kind/tzid of an occurrence when computing its
// end instant. For TZID values the duration is added on the absolute timeline
// (matching how a meeting "1 hour long" behaves across a DST transition).
func shiftTimeValue(start TimeValue, d time.Duration) TimeValue {
	end := TimeValue{
		Time:     start.Time.Add(d),
		Kind:     start.Kind,
		TZID:     start.TZID,
		DateOnly: start.DateOnly,
	}
	switch start.Kind {
	case TimeKindTZID:
		if loc, err := time.LoadLocation(start.TZID); err == nil {
			end.Wall = end.Time.In(loc)
		} else {
			end.Wall = end.Time.UTC()
		}
	case TimeKindFloating:
		end.Wall = end.Time.UTC()
	default:
		end.Wall = end.Time
	}
	return end
}

// parseISODuration parses the RFC 5545 dur-value subset:
// [+/-]P[nW][nD][T[nH][nM][nS]]. Weeks and the time components are mutually
// exclusive per the grammar; date+time combinations are supported.
func parseISODuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	if len(s) == 0 || s[0] != 'P' {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	s = s[1:]
	var total time.Duration
	num := ""
	inTime := false
	flush := func(unit byte) error {
		n, err := strconv.Atoi(num)
		if err != nil {
			return fmt.Errorf("invalid duration number %q", num)
		}
		num = ""
		switch unit {
		case 'W':
			total += time.Duration(n) * 7 * 24 * time.Hour
		case 'D':
			total += time.Duration(n) * 24 * time.Hour
		case 'H':
			total += time.Duration(n) * time.Hour
		case 'M':
			total += time.Duration(n) * time.Minute
		case 'S':
			total += time.Duration(n) * time.Second
		default:
			return fmt.Errorf("invalid duration unit %q", string(unit))
		}
		return nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == 'T':
			inTime = true
		case c >= '0' && c <= '9':
			num += string(c)
		case (c == 'W' || c == 'D') && !inTime:
			if err := flush(c); err != nil {
				return 0, err
			}
		case (c == 'H' || c == 'M' || c == 'S') && inTime:
			if err := flush(c); err != nil {
				return 0, err
			}
		default:
			return 0, fmt.Errorf("invalid duration character %q in %q", string(c), s)
		}
	}
	if num != "" {
		return 0, fmt.Errorf("duration %q missing unit", s)
	}
	if neg {
		total = -total
	}
	return total, nil
}
