package ics

import (
	"sort"
	"time"
)

// This file contains the internal RRULE expansion engine used by ExpandEvent.
//
// The engine walks RFC 5545 recurrence periods (the FREQ unit scaled by
// INTERVAL), enumerates candidate wall clocks inside each period, filters them
// by the BYxxx rule parts, applies BYSETPOS, and localizes each surviving wall
// clock through the same temporal semantics as DTSTART (TZID, UTC, floating, or
// date). Wall clock arithmetic is deliberately performed on "naive" times held
// in time.UTC so calendar fields behave independently of host zone data; the
// final localization reintroduces DST behaviour.

type ruleGenerator struct {
	rule        *RecurrenceRule
	start       TimeValue
	cfg         *expansionConfig
	loc         *time.Location // localization target
	startWall   time.Time      // naive wall clock of DTSTART (UTC-located)
	dateOnly    bool
	periodIdx   int
	seq         int
	emitted     int
	pending     []time.Time // naive wall clocks queued for the current period
	done        bool
	truncated   bool
	truncReason TruncatedReason
	periodsRun  int
	budget      int
	jumped      bool
}

func newRuleGenerator(rule *RecurrenceRule, start TimeValue, cfg *expansionConfig) *ruleGenerator {
	g := &ruleGenerator{
		rule:   rule,
		start:  start,
		cfg:    cfg,
		loc:    localizationLocation(start, cfg),
		budget: cfg.maxInstances*12 + 10000,
	}
	g.dateOnly = start.DateOnly
	g.startWall = naiveWall(start)
	return g
}

// localizationLocation returns the location used to turn naive wall clocks back
// into absolute instants.
func localizationLocation(tv TimeValue, cfg *expansionConfig) *time.Location {
	switch tv.Kind {
	case TimeKindTZID:
		if loc, err := time.LoadLocation(tv.TZID); err == nil {
			return loc
		}
		return time.UTC
	case TimeKindFloating:
		return cfg.floatingLoc
	default:
		return time.UTC
	}
}

// naiveWall extracts the source wall clock as a UTC-located naive time.
func naiveWall(tv TimeValue) time.Time {
	w := tv.Wall
	return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, time.UTC)
}

// untilTime returns the absolute UNTIL bound (inclusive), or the zero time.
// Date-only UNTIL covers the whole final day.
func (g *ruleGenerator) untilTime() time.Time {
	if g.rule.Until.IsZero() {
		return time.Time{}
	}
	u := g.rule.Until
	if g.rule.UntilDateOnly {
		return time.Date(u.Year(), u.Month(), u.Day(), 23, 59, 59, 0, g.loc)
	}
	if u.Location() == time.UTC {
		return u
	}
	// Naively written UNTIL: interpret in the rule's local zone.
	return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), u.Minute(), u.Second(), 0, g.loc)
}

// localize converts a naive wall clock into the occurrence TimeValue,
// preserving the DTSTART semantics.
func (g *ruleGenerator) localize(wall time.Time) TimeValue {
	t := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, g.loc)
	tv := TimeValue{Time: t, Kind: g.start.Kind, TZID: g.start.TZID, DateOnly: g.dateOnly, Wall: wall}
	return tv
}

// stopPast returns true when an absolute instant is beyond UNTIL.
func (g *ruleGenerator) pastUntil(t time.Time) bool {
	u := g.untilTime()
	return !u.IsZero() && t.After(u)
}

// next returns the next rule occurrence. DTSTART is emitted as a rule
// occurrence only when it satisfies the rule predicate (RFC 5545 3.3.10:
// DTSTART counts toward COUNT exactly in that case); the master assembly adds
// DTSTART unconditionally as SourceDTStart regardless.
func (g *ruleGenerator) next() (tv TimeValue, seq int, more bool, err error) {
	if g.done || g.truncated {
		return TimeValue{}, 0, false, nil
	}

	for {
		if len(g.pending) == 0 {
			if !g.fillNextPeriod() {
				g.done = true
				return TimeValue{}, 0, false, nil
			}
		}
		wall := g.pending[0]
		g.pending = g.pending[1:]
		localized := g.localize(wall)

		// UNTIL is evaluated on the absolute instant.
		if g.pastUntil(localized.Time) {
			g.done = true
			return TimeValue{}, 0, false, nil
		}
		// Everything beyond the window end can never be consumed.
		if g.cfg.window.pastEnd(localized.Time) {
			g.done = true
			return TimeValue{}, 0, false, nil
		}
		g.seq++
		g.emitted++
		if g.rule.Count != 0 && g.emitted >= g.rule.Count {
			g.done = true
			return localized, g.seq, false, nil
		}
		// Memory protection for dense rules (e.g. SECONDLY) inside a long
		// window: the caller-level cap is per merged event, but one unbounded
		// rule should not queue unbounded candidates either.
		if g.emitted > g.cfg.maxInstances+1 {
			g.truncated = true
			g.truncReason = TruncatedMaxInstances
			// Stream ends here; the caller-level cap performs the actual trim.
			g.done = true
			return localized, g.seq, false, nil
		}
		return localized, g.seq, true, nil
	}
}

// fillNextPeriod advances to the following recurrence period and populates
// pending with ascending candidate wall clocks. It returns false on natural
// termination or truncation.
func (g *ruleGenerator) fillNextPeriod() bool {
	for {
		// Dense FREQ rules (SECONDLY/MINUTELY/HOURLY) skip directly to the
		// period containing the window start. COUNT rules cannot jump because
		// COUNT also counts pre-window occurrences.
		if g.periodIdx == 0 && !g.jumped {
			g.jumped = true
			if !g.cfg.window.From.IsZero() && g.rule.Count == 0 {
				if jump, ok := g.subDailyPeriodJump(); ok {
					if jump < 0 {
						jump = 0
					}
					g.periodIdx = jump
					continue
				}
			}
		}
		idx := g.periodIdx
		g.periodIdx++
		g.periodsRun++
		if g.periodsRun > g.budget {
			g.truncated = true
			g.truncReason = TruncatedBudget
			return false
		}

		// Terminate before enumerating once the period start is past UNTIL or
		// the window end. This also terminates rules whose BY-parts never match
		// (e.g. FREQ=MONTHLY;BYMONTHDAY=30 against February).
		periodStart := g.periodStartNaive(idx)
		if localizedStart := g.localize(periodStart); g.pastUntil(localizedStart.Time) {
			return false
		} else if g.cfg.window.pastEnd(localizedStart.Time) {
			return false
		}

		var walls []time.Time
		if g.isSubDaily() {
			walls = g.enumerateSubDailyDay(idx)
		} else {
			walls = g.enumerateCalendarPeriod(idx)
		}

		// Ascending order and de-duplication of wall clocks within the period
		// (a candidate can match several BY-parts, e.g. BYDAY=MO,MO).
		sort.Slice(walls, func(i, j int) bool { return walls[i].Before(walls[j]) })
		walls = dedupeWalls(walls)

		// The first recurrence period may contain walls before DTSTART (e.g.
		// BYDAY in DTSTART's week) or that simply fail the rule predicate
		// (e.g. BYHOUR excluding DTSTART's hour). Discard every wall earlier
		// than DTSTART; walls equal/later survive on their own predicate result.
		// The master always adds SourceDTStart separately, so DTSTART appears
		// exactly once with an RRULE source only when it really matches.
		if idx == 0 {
			filtered := walls[:0]
			for _, w := range walls {
				if !w.Before(g.startWall) {
					filtered = append(filtered, w)
				}
			}
			walls = filtered
		}

		if len(walls) == 0 {
			// Empty period (BY-parts did not match any day, e.g. BYMONTHDAY=30
			// in February): advance to the next period.
			continue
		}

		// Fast-forward: drop entire periods ending before the window start.
		last := g.localize(walls[len(walls)-1])
		if !g.cfg.window.From.IsZero() && last.Time.Before(g.cfg.window.From) {
			// COUNT counts pre-window occurrences too; skipping a period must
			// consume its emitted matches.
			if g.rule.Count != 0 {
				g.emitted += len(walls)
				g.seq += len(walls)
				if g.emitted >= g.rule.Count {
					g.done = true
					return false
				}
			}
			continue
		}
		// A period straddling the window start: drop and count only the walls
		// that precede it.
		if !g.cfg.window.From.IsZero() {
			leading := 0
			for _, w := range walls {
				if g.localize(w).Time.Before(g.cfg.window.From) {
					leading++
				} else {
					break
				}
			}
			if leading > 0 {
				walls = walls[leading:]
				if g.rule.Count != 0 {
					g.emitted += leading
					g.seq += leading
					if g.emitted >= g.rule.Count {
						g.done = true
						return false
					}
				}
			}
		}
		first := g.localize(walls[0])
		if g.pastUntil(first.Time) {
			return false
		}
		g.pending = walls
		return true
	}
}

func dedupeWalls(in []time.Time) []time.Time {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, w := range in[1:] {
		if !w.Equal(out[len(out)-1]) {
			out = append(out, w)
		}
	}
	return out
}
