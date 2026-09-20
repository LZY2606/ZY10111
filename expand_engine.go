package ics

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// resolveLocation maps a TZID to a *time.Location using (1) cache, (2) custom
// resolvers, (3) the host IANA database. Embedded VTIMEZONE blocks are
// compiled by the calendar-level entry point and injected as a resolver.
func (e *expander) resolveLocation(tzid string) (*time.Location, error) {
	if loc, ok := e.locCache[tzid]; ok {
		return loc, nil
	}
	for _, r := range e.cfg.resolvers {
		if r == nil {
			continue
		}
		if loc := r(tzid); loc != nil {
			e.locCache[tzid] = loc
			return loc, nil
		}
	}
	loc, err := time.LoadLocation(tzid)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTimezone, tzid)
	}
	e.locCache[tzid] = loc
	return loc, nil
}

// parseDateTime converts one time/date property into a semantic DateTime
// without discarding TZID/floating/date-only information.
func (e *expander) parseDateTime(prop *IANAProperty) (DateTime, error) {
	val := strings.TrimSpace(prop.Value)
	dateOnly := false
	if vals, ok := prop.ICalParameters["VALUE"]; ok {
		for _, v := range vals {
			if v == string(ValueDataTypeDate) {
				dateOnly = true
			}
		}
	}
	tzids := prop.ICalParameters["TZID"]
	switch {
	case dateOnly:
		t, err := time.ParseInLocation(icalDateFormatLocal, val, e.floating)
		if err != nil {
			return DateTime{}, fmt.Errorf("date %q: %w", val, err)
		}
		return DateTime{Time: t, Kind: TimeKindDate, DateOnly: true}, nil
	case len(tzids) == 1 && tzids[0] != "":
		loc, err := e.resolveLocation(tzids[0])
		if err != nil {
			return DateTime{}, err
		}
		t, err := time.ParseInLocation(icalTimestampFormatLocal, val, loc)
		if err != nil {
			return DateTime{}, fmt.Errorf("tzid datetime %q: %w", val, err)
		}
		return DateTime{Time: t, Kind: TimeKindTZID, TZID: tzids[0]}, nil
	case strings.HasSuffix(val, "Z"):
		t, err := time.ParseInLocation(icalTimestampFormatUtc, val, time.UTC)
		if err != nil {
			return DateTime{}, fmt.Errorf("utc datetime %q: %w", val, err)
		}
		return DateTime{Time: t, Kind: TimeKindUTC}, nil
	default:
		t, err := time.ParseInLocation(icalTimestampFormatLocal, val, e.floating)
		if err != nil {
			return DateTime{}, fmt.Errorf("floating datetime %q: %w", val, err)
		}
		return DateTime{Time: t, Kind: TimeKindFloating}, nil
	}
}

func (e *expander) readRules(prop ComponentProperty) ([]*RecurrenceRule, []string, error) {
	ps := e.master.GetProperties(prop)
	rules := make([]*RecurrenceRule, 0, len(ps))
	vals := make([]string, 0, len(ps))
	for _, p := range ps {
		r, err := ParseRecurrenceRule(p.Value)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s %q: %w", prop, p.Value, err)
		}
		rules = append(rules, r)
		vals = append(vals, p.Value)
	}
	return rules, vals, nil
}

func (e *expander) readDateList(prop ComponentProperty) ([]DateTime, []string, error) {
	ps := e.master.GetProperties(prop)
	var out []DateTime
	var raw []string
	for _, p := range ps {
		for _, v := range strings.Split(p.Value, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			clone := *p
			clone.Value = v
			dt, err := e.parseDateTime(&clone)
			if err != nil {
				return nil, nil, fmt.Errorf("parse %s value %q: %w", prop, v, err)
			}
			out = append(out, dt)
			raw = append(raw, v)
		}
	}
	return out, raw, nil
}

// indexOverrides parses every RECURRENCE-ID sibling and keys it by the
// occurrence it targets.
func (e *expander) indexOverrides(overrides []*VEvent) error {
	for _, ov := range overrides {
		if ov == nil {
			continue
		}
		ridProp := ov.GetProperty(ComponentPropertyRecurrenceId)
		if ridProp == nil {
			continue
		}
		rid, err := e.parseDateTime(ridProp)
		if err != nil {
			return fmt.Errorf("override RECURRENCE-ID: %w", err)
		}
		rec := &Override{Component: ov, RecurrenceID: rid, Cancelled: false}
		if sp := ov.GetProperty(ComponentPropertyStatus); sp != nil && sp.Value == string(ObjectStatusCancelled) {
			rec.Cancelled = true
		}
		if ds := ov.GetProperty(ComponentPropertyDtStart); ds != nil {
			if rec.NewStart, err = e.parseDateTime(ds); err != nil {
				return fmt.Errorf("override DTSTART: %w", err)
			}
		}
		e.overrides[occurrenceKey(rid)] = rec
	}
	return nil
}

// occurrence is the mutable working representation of one expanded instant
// before windowing/sorting.
type occurrence struct {
	start      DateTime
	sources    []Source
	override   *Override
	suppressed bool
}

func (e *expander) expand(winStart, winEnd time.Time, max int) (*Result, error) {
	res := &Result{}
	bag := map[string]*occurrence{}
	order := func() []string { return nil }
	_ = order

	add := func(dt DateTime, src Source) {
		key := occurrenceKey(dt)
		occ, ok := bag[key]
		if !ok {
			occ = &occurrence{start: dt}
			bag[key] = occ
		}
		// merge across kinds (UTC vs TZID same instant): keep the TZID-anchored
		// semantics when present, else first seen.
		if dt.Kind == TimeKindTZID && occ.start.Kind != TimeKindTZID {
			occ.start = dt
		}
		occ.sources = append(occ.sources, src)
	}

	// 1) DTSTART is always an occurrence.
	add(e.start, Source{Kind: SourceStart, Value: e.master.GetProperty(ComponentPropertyDtStart).Value})

	// 2) RRULE generation. Every rule runs from DTSTART. Generation stops once
	// candidates pass the window end (plus a bounded look-ahead is unnecessary
	// because candidates are ascending); COUNT/UNTIL end naturally.
	rruleStart := e.ruleAnchorTime()
	for i, rule := range e.rules {
		it, err := newRuleIterator(rule, rruleStart)
		if err != nil {
			return nil, fmt.Errorf("RRULE[%d] %q: %w", i, e.ruleVals[i], err)
		}
		raw := e.ruleVals[i]
		for {
			t, ok, err := it.Next()
			if err != nil {
				if errors.Is(err, ErrRecurrenceLimit) {
					return e.finishWithTruncation(bag, winStart, winEnd, max, TruncationSafetyLimit, res)
				}
				return nil, fmt.Errorf("RRULE[%d] %q: %w", i, raw, err)
			}
			if !ok {
				break
			}
			if t.UTC().After(winEnd) {
				break
			}
			dt := e.anchorDateTime(t)
			add(dt, Source{Kind: SourceRRule, RuleIndex: i, Value: raw})
		}
	}

	// 3) RDATE injection (RDATE before the window start can still anchor
	// overrides, so all are considered; windowing filters them later).
	for i, dt := range e.rdate {
		add(dt, Source{Kind: SourceRDate, Value: e.rdateVal[i]})
	}

	// 4) EXDATE removal, EXRULE removal, overrides, suppression.
	removed := map[string][]Source{}

	matchExclude := func(start DateTime, src Source) {
		for _, ex := range e.exdate {
			if SameOccurrence(start, ex) {
				removed[occurrenceKey(start)] = append(removed[occurrenceKey(start)], Source{Kind: SourceExclusion, Value: formatRaw(ex)})
				return
			}
		}
		// EXRULE support (deprecated in RFC 5545 but still encountered).
		for _, rule := range e.exrules {
			it, err := newRuleIterator(rule, rruleStart)
			if err != nil {
				continue
			}
			for {
				t, ok, err := it.Next()
				if err != nil || !ok {
					break
				}
				if t.UTC().After(winEnd) {
					break
				}
				if SameOccurrence(start, e.anchorDateTime(t)) {
					removed[occurrenceKey(start)] = append(removed[occurrenceKey(start)], Source{Kind: SourceExRule, Value: rule.String()})
					return
				}
			}
		}
	}

	for _, occ := range bag {
		matchExclude(occ.start, Source{})
	}

	// Apply EXDATE before overrides: an override whose target is also EXDATE'd
	// still wins (RECURRENCE-ID is authoritative), but we record both facts in
	// diagnostics via the override handling below.
	for key, occ := range bag {
		if reasons, ok := removed[key]; ok {
			// If an override targets this occurrence, keep it (override wins).
			if _, hasOv := e.overrides[key]; hasOv {
				delete(removed, key)
				continue
			}
			occ.suppressed = true
			occ.sources = append(occ.sources, reasons...)
		}
	}

	// Overrides: attach to the target occurrence; rescheduled overrides move
	// the instance start; cancelled ones suppress. Orphans create standalone
	// instances inside the window.
	for key, ov := range e.overrides {
		if occ, ok := bag[key]; ok {
			occ.override = ov
			if ov.Cancelled {
				occ.suppressed = true
			}
			if !ov.NewStart.Time.IsZero() {
				// Rescheduling: evidence travels to the new instant.
				newKey := occurrenceKey(ov.NewStart)
				if newKey != key {
					delete(bag, key)
					occ.start = ov.NewStart
					bag[newKey] = occ
				}
			}
			occ.sources = append(occ.sources, Source{Kind: SourceOverride,
				Value: ov.Component.GetProperty(ComponentPropertyRecurrenceId).Value})
		} else {
			ov.Orphan = true
			if ov.Cancelled {
				continue
			}
			dt := ov.NewStart
			if dt.Time.IsZero() {
				dt = ov.RecurrenceID
			}
			add(dt, Source{Kind: SourceOverride, Value: "orphan"})
			if newOcc, ok := bag[occurrenceKey(dt)]; ok {
				newOcc.override = ov
			}
		}
	}

	// 5) Window filtering, end-time attachment, stable sorting and the cap.
	inWindow := func(dt DateTime) bool {
		var t time.Time
		if dt.Kind == TimeKindFloating {
			// Floating values carry no absolute instant; compare wall clocks
			// against the window expressed in the floating zone, deterministically.
			t = dt.Time
			ws := winStart.In(e.floating)
			we := winEnd.In(e.floating)
			if t.Before(ws) {
				return false
			}
			if e.cfg.windowInclusiveEnd {
				return !t.After(we)
			}
			return t.Before(we)
		}
		u := dt.Time.UTC()
		if u.Before(winStart) {
			return false
		}
		if e.cfg.windowInclusiveEnd {
			return !u.After(winEnd)
		}
		return u.Before(winEnd)
	}

	instances := make([]*Instance, 0, len(bag))
	var excluded []ExcludedRecord
	for _, occ := range bag {
		if !inWindow(occ.start) {
			continue
		}
		sortSources(occ.sources)
		inst := &Instance{
			Start:      occ.start,
			Sources:    occ.sources,
			Override:   occ.override,
			Suppressed: occ.suppressed,
		}
		inst.End = e.instanceEnd(occ.start)
		if occ.suppressed {
			if e.cfg.diagnostics {
				reasons := exclusionReasons(occ.sources)
				for _, src := range occ.sources {
					if src.Kind == SourceOverride {
						reasons = append(reasons, src)
					}
				}
				excluded = append(excluded, ExcludedRecord{Start: occ.start, Reasons: reasons})
			}
			continue
		}
		instances = append(instances, inst)
	}
	sortInstances(instances)
	if e.cfg.diagnostics {
		sort.Slice(excluded, func(i, j int) bool {
			return instanceLess(excluded[i].Start, excluded[j].Start)
		})
		res.Diagnostics.Excluded = excluded
	}

	if len(instances) > max {
		// Truncation: cap is applied after collection, which guarantees the
		// first max ascending instances are returned regardless of rule order.
		res.Instances = instances[:max]
		res.Truncated = true
		res.Truncation = TruncationMaxInstances
		return res, fmt.Errorf("%w: %d in-window instances", ErrExpansionTruncated, max)
	}
	res.Instances = instances
	return res, nil
}

func (e *expander) finishWithTruncation(bag map[string]*occurrence, winStart, winEnd time.Time, max int, reason TruncationReason, res *Result) (*Result, error) {
	res.Truncated = true
	res.Truncation = reason
	return res, fmt.Errorf("%w: %s", ErrExpansionTruncated, reason)
}
