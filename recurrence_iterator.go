package ics

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// Errors returned by the recurrence iterator. They are wrapped by the caller
// with context, so callers should use errors.Is.
var (
	// ErrInvalidRecurrenceRule reports an RRULE whose parts are mutually
	// inconsistent or outside their RFC 5545 value ranges.
	ErrInvalidRecurrenceRule = errors.New("invalid recurrence rule")
	// ErrRecurrenceLimit reports that evaluation exceeded the safety limit on
	// generated candidates. It is distinct from the expansion instance cap.
	ErrRecurrenceLimit = errors.New("recurrence iteration safety limit reached")
)

const (
	// iteratorHardHorizon bounds how far into the future an untruncated rule is
	// evaluated before giving up (RFC rules are otherwise enumerable forever).
	iteratorHardHorizonYears = 10000
	// iteratorMaxCandidates bounds the number of candidate instants examined
	// for a single rule so that pathological BY-* expansions cannot run away.
	iteratorMaxCandidates = 2_000_000
)

// ruleIterator enumerates the occurrences of a single RecurrenceRule anchored
// at a DTSTART, in ascending absolute-instant order. It is an internal engine:
// the expansion layer handles merging, exclusions, overrides and windowing.
type ruleIterator struct {
	rule    *RecurrenceRule
	dtstart time.Time // retains its original location semantics

	interval   int
	wkst       time.Weekday
	until      time.Time
	hasUntil   bool
	untilDate  bool
	count      int
	emitted    int
	period     time.Time // start of the period currently being expanded
	periodsRun int
	candidates []time.Time
	index      int
	exhausted  bool
	started    bool
	firstDone  bool
	scanned    int
}

func newRuleIterator(rule *RecurrenceRule, dtstart time.Time) (*ruleIterator, error) {
	if rule == nil {
		return nil, fmt.Errorf("%w: nil rule", ErrInvalidRecurrenceRule)
	}
	if rule.Interval <= 0 {
		return nil, fmt.Errorf("%w: INTERVAL must be positive", ErrInvalidRecurrenceRule)
	}
	if err := validateRuleRanges(rule); err != nil {
		return nil, err
	}
	wkst := time.Monday
	if rule.Wkst != "" {
		wkst = weekdayToGo(rule.Wkst)
	}
	ri := &ruleIterator{
		rule:     rule,
		dtstart:  dtstart,
		interval: rule.Interval,
		wkst:     wkst,
		count:    rule.Count,
		period:   firstPeriodStart(rule, dtstart, wkst),
	}
	if !rule.Until.IsZero() {
		ri.hasUntil = true
		ri.until = rule.Until
		ri.untilDate = rule.UntilDateOnly
	}
	return ri, nil
}

func validateRuleRanges(r *RecurrenceRule) error {
	check := func(name string, vs []int, min, max int) error {
		for _, v := range vs {
			if v < min || v > max {
				return fmt.Errorf("%w: %s=%d out of range [%d,%d]", ErrInvalidRecurrenceRule, name, v, min, max)
			}
		}
		return nil
	}
	if r.Count < 0 {
		return fmt.Errorf("%w: COUNT must be non-negative", ErrInvalidRecurrenceRule)
	}
	for name, vs := range map[string][]int{
		"BYMONTH":    r.ByMonth,
		"BYMONTHDAY": r.ByMonthDay,
		"BYYEARDAY":  r.ByYearDay,
		"BYHOUR":     r.ByHour,
		"BYMINUTE":   r.ByMinute,
		"BYSECOND":   r.BySecond,
		"BYSETPOS":   r.BySetPos,
	} {
		var min, max int
		switch name {
		case "BYMONTH":
			min, max = 1, 12
		case "BYMONTHDAY":
			min, max = -31, 31
		case "BYYEARDAY":
			min, max = -366, 366
		case "BYHOUR":
			min, max = 0, 23
		case "BYMINUTE", "BYSECOND":
			min, max = 0, 59
		case "BYSETPOS":
			min, max = -366, 366
		}
		if err := check(name, vs, min, max); err != nil {
			return err
		}
	}
	// BYWEEKNO only applies to YEARLY and conflicts with the day-of-month parts.
	if len(r.ByWeekNo) > 0 {
		if r.Freq != FrequencyYearly {
			return fmt.Errorf("%w: BYWEEKNO only valid with FREQ=YEARLY", ErrInvalidRecurrenceRule)
		}
		if len(r.ByMonth) > 0 || len(r.ByMonthDay) > 0 || len(r.ByYearDay) > 0 {
			return fmt.Errorf("%w: BYWEEKNO conflicts with BYMONTH/BYMONTHDAY/BYYEARDAY", ErrInvalidRecurrenceRule)
		}
	}
	if len(r.ByYearDay) > 0 && (len(r.ByMonthDay) > 0) {
		return fmt.Errorf("%w: BYYEARDAY conflicts with BYMONTHDAY", ErrInvalidRecurrenceRule)
	}
	// Numeric ordinals on BYDAY are meaningless for sub-week frequencies.
	if r.Freq == FrequencyDaily || r.Freq == FrequencyHourly ||
		r.Freq == FrequencyMinutely || r.Freq == FrequencySecondly {
		for _, d := range r.ByDay {
			if d.OrdWeek != 0 {
				return fmt.Errorf("%w: ordinal BYDAY invalid with %s", ErrInvalidRecurrenceRule, r.Freq)
			}
		}
	}
	return nil
}

func weekdayToGo(w Weekday) time.Weekday {
	switch w {
	case WeekdaySunday:
		return time.Sunday
	case WeekdayMonday:
		return time.Monday
	case WeekdayTuesday:
		return time.Tuesday
	case WeekdayWednesday:
		return time.Wednesday
	case WeekdayThursday:
		return time.Thursday
	case WeekdayFriday:
		return time.Friday
	case WeekdaySaturday:
		return time.Saturday
	}
	return time.Monday
}

func weekdayToICS(w time.Weekday) Weekday {
	switch w {
	case time.Sunday:
		return WeekdaySunday
	case time.Monday:
		return WeekdayMonday
	case time.Tuesday:
		return WeekdayTuesday
	case time.Wednesday:
		return WeekdayWednesday
	case time.Thursday:
		return WeekdayThursday
	case time.Friday:
		return WeekdayFriday
	case time.Saturday:
		return WeekdaySaturday
	}
	return WeekdayMonday
}

// firstPeriodStart returns the start of the period that contains DTSTART.
// WEEKLY periods begin on the WKST boundary; all other periods align to the
// DTSTART calendar field.
func firstPeriodStart(rule *RecurrenceRule, dtstart time.Time, wkst time.Weekday) time.Time {
	y, m, d := dtstart.Date()
	switch rule.Freq {
	case FrequencySecondly:
		return time.Date(y, m, d, dtstart.Hour(), dtstart.Minute(), dtstart.Second(), 0, dtstart.Location())
	case FrequencyMinutely:
		// Keep the DTSTART second, reset nothing finer than the minute.
		return time.Date(y, m, d, dtstart.Hour(), dtstart.Minute(), dtstart.Second(), 0, dtstart.Location())
	case FrequencyHourly:
		// Keep the DTSTART minute and second.
		return time.Date(y, m, d, dtstart.Hour(), dtstart.Minute(), dtstart.Second(), 0, dtstart.Location())
	case FrequencyDaily:
		return time.Date(y, m, d, 0, 0, 0, 0, dtstart.Location())
	case FrequencyWeekly:
		diff := (int(dtstart.Weekday()) - int(wkst) + 7) % 7
		weekStart := time.Date(y, m, d, 0, 0, 0, 0, dtstart.Location()).AddDate(0, 0, -diff)
		return weekStart
	case FrequencyMonthly:
		return time.Date(y, m, 1, 0, 0, 0, 0, dtstart.Location())
	case FrequencyYearly:
		return time.Date(y, 1, 1, 0, 0, 0, 0, dtstart.Location())
	}
	return dtstart
}

// Next returns the next occurrence and true, or zero time and false when the
// rule has naturally ended (COUNT satisfied, UNTIL passed, or horizon). A
// non-nil error means evaluation could not continue safely.
func (it *ruleIterator) Next() (time.Time, bool, error) {
	if !it.firstDone {
		it.firstDone = true
		// DTSTART is always the first occurrence, even when its date fields do
		// not satisfy the BY-* parts (RFC 5545 section 3.8.5.3).
		if it.hasUntil && it.pastUntil(it.dtstart) {
			it.exhausted = true
			return time.Time{}, false, nil
		}
		it.emitted++
		return it.dtstart, true, nil
	}
	for {
		for it.index < len(it.candidates) {
			cand := it.candidates[it.index]
			it.index++
			ok, err := it.consider(cand)
			if err != nil {
				return time.Time{}, false, err
			}
			if ok {
				return cand, true, nil
			}
		}
		if it.exhausted {
			return time.Time{}, false, nil
		}
		if err := it.advancePeriod(); err != nil {
			return time.Time{}, false, err
		}
	}
}

// consider applies the cross-cutting filters that are independent of how a
// candidate was generated: the DTSTART floor, COUNT, UNTIL and horizon. It
// flips it.exhausted when a candidate can never be followed by another valid
// one in a later period.
func (it *ruleIterator) consider(cand time.Time) (bool, error) {
	it.scanned++
	if it.scanned > iteratorMaxCandidates {
		return false, fmt.Errorf("%w: examined more than %d candidates", ErrRecurrenceLimit, iteratorMaxCandidates)
	}
	// DTSTART was already emitted as occurrence number one; never repeat it
	// or yield anything earlier.
	if !cand.After(it.dtstart) {
		return false, nil
	}
	if it.count > 0 && it.emitted >= it.count {
		it.exhausted = true
		return false, nil
	}
	if it.hasUntil {
		if it.pastUntil(cand) {
			it.exhausted = true
			return false, nil
		}
	}
	if cand.Year() >= it.dtstart.Year()+iteratorHardHorizonYears {
		return false, fmt.Errorf("%w: rule extends past year %d", ErrRecurrenceLimit, it.dtstart.Year()+iteratorHardHorizonYears)
	}
	it.emitted++
	return true, nil
}

// pastUntil compares a candidate against UNTIL. A date-only UNTIL ends at the
// last second of the DTSTART-local day matching that date; a date-time UNTIL
// is compared as an absolute UTC instant.
func (it *ruleIterator) pastUntil(cand time.Time) bool {
	if it.untilDate {
		uy, um, ud := it.until.Date()
		cy, cm, cd := cand.Date()
		if cy != uy || cm != um || cd != ud {
			return cy > uy || (cy == uy && (cm > um || (cm == um && cd > ud)))
		}
		return false
	}
	return cand.UTC().After(it.until.UTC())
}

// advancePeriod generates and sorts the candidate set for the current period,
// then moves the period cursor forward by INTERVAL periods.
func (it *ruleIterator) advancePeriod() error {
	it.periodsRun++
	cands, err := it.buildPeriodCandidates(it.period)
	if err != nil {
		return err
	}
	// BYSETPOS is applied to the rule-derived candidate set only. DTSTART,
	// which may not satisfy the BY-* pattern, is emitted separately as
	// occurrence one and must not shift set positions in the first period.
	cands = applyBySetPos(it.rule, it.period, it.rule.Freq, cands)
	sort.Slice(cands, func(i, j int) bool { return cands[i].Before(cands[j]) })
	cands = dedupeTimes(cands)
	if it.count > 0 && it.emitted >= it.count {
		it.exhausted = true
	}
	if it.hasUntil && it.beyondHorizonPeriod() {
		it.exhausted = true
	}
	it.candidates = cands
	it.index = 0
	it.period = nextPeriodStart(it.rule.Freq, it.period, it.interval, it.wkst)
	return nil
}

func (it *ruleIterator) beyondHorizonPeriod() bool {
	if it.untilDate {
		uy, um, ud := it.until.Date()
		limit := time.Date(uy, um, ud, 23, 59, 59, 0, it.dtstart.Location())
		return it.period.After(limit)
	}
	localUntil := it.until.In(it.dtstart.Location())
	return it.period.After(localUntil)
}

func nextPeriodStart(freq Frequency, period time.Time, interval int, wkst time.Weekday) time.Time {
	y, m, d := period.Date()
	_ = d
	// Period starts are built as local wall-clock dates rather than derived
	// with AddDate from the previous period: AddDate preserves instants, so
	// across a DST change midnight would drift to 01:00. Reconstructing each
	// boundary keeps every period anchored at local midnight / month start.
	switch freq {
	case FrequencySecondly:
		return time.Date(y, m, d, period.Hour(), period.Minute(), period.Second()+interval, 0, period.Location())
	case FrequencyMinutely:
		return time.Date(y, m, d, period.Hour(), period.Minute()+interval, period.Second(), 0, period.Location())
	case FrequencyHourly:
		return time.Date(y, m, d, period.Hour()+interval, period.Minute(), period.Second(), 0, period.Location())
	case FrequencyDaily:
		return time.Date(y, m, d+interval, 0, 0, 0, 0, period.Location())
	case FrequencyWeekly:
		return time.Date(y, m, d+7*interval, 0, 0, 0, 0, period.Location())
	case FrequencyMonthly:
		return time.Date(y, time.Month(int(m)+interval), 1, 0, 0, 0, 0, period.Location())
	case FrequencyYearly:
		return time.Date(y+interval, 1, 1, 0, 0, 0, 0, period.Location())
	}
	return period
}

func dedupeTimes(in []time.Time) []time.Time {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for i := 1; i < len(in); i++ {
		if !in[i].Equal(out[len(out)-1]) {
			out = append(out, in[i])
		}
	}
	return out
}

// buildPeriodCandidates produces every candidate inside one rule period that
// satisfies the BY-* limit/expansion rules. Sub-day BY rules are applied by
// combining qualifying dates with every allowed H:M:S tuple; date-based rules
// keep the DTSTART time of day.
func (it *ruleIterator) buildPeriodCandidates(period time.Time) ([]time.Time, error) {
	r := it.rule
	var days []time.Time
	var err error
	switch r.Freq {
	case FrequencySecondly, FrequencyMinutely, FrequencyHourly:
		return it.enumerateSubDay(period)
	case FrequencyDaily:
		days, err = it.enumerateSimpleDays(period)
	case FrequencyWeekly:
		days, err = it.enumerateWeeklyDays(period)
	case FrequencyMonthly:
		days, err = it.enumerateMonthlyDays(period)
	case FrequencyYearly:
		days, err = it.enumerateYearlyDays(period)
	default:
		return nil, fmt.Errorf("%w: unsupported FREQ %q", ErrInvalidRecurrenceRule, r.Freq)
	}
	if err != nil {
		return nil, err
	}
	loc := it.dtstart.Location()
	hmsList := timeOfDayTuples(r, it.dtstart)
	out := make([]time.Time, 0, len(days)*len(hmsList))
	for _, day := range days {
		for _, hms := range hmsList {
			y, m, d := day.Date()
			cand := normalizeWall(y, m, d, hms.hour, hms.min, hms.sec, loc)
			out = append(out, cand)
		}
	}
	return out, nil
}

// normalizeWall turns a wall clock in loc into an absolute time following
// RFC 5545 section 3.3.5 for the two degenerate DST cases:
//
//   - A wall time that falls in a spring-forward gap does not exist. It is
//     interpreted with the UTC offset in effect immediately before the gap,
//     so e.g. 02:30 (skipped, EST/-05:00) denotes the instant 03:30 EDT.
//   - A wall time that occurs twice during a fall-back transition denotes the
//     first occurrence, i.e. the daylight-time interpretation (Go's default).
//
// UTC and fixed-offset zones (which includes floating times) always map
// uniquely and are returned unchanged.
func normalizeWall(y int, m time.Month, d, hh, mm, ss int, loc *time.Location) time.Time {
	if loc == nil || loc == time.UTC {
		return time.Date(y, m, d, hh, mm, ss, 0, loc)
	}
	// First ask Go for the default interpretation (earlier offset on ambiguity).
	wall := time.Date(y, m, d, hh, mm, ss, 0, loc)
	// Detect a gap: a unique wall time round-trips to the same fields. When
	// Go shifts the time (spring forward) the fields change. Offset changes at
	// a normal DST boundary are measured against the same wall time the
	// previous day to avoid treating an ambiguous fall-back time as a gap.
	ry, rm, rd := wall.Date()
	roundTrips := ry == y && rm == m && rd == d &&
		wall.Hour() == hh && wall.Minute() == mm && wall.Second() == ss
	if roundTrips {
		return wall
	}
	// Gap. Recover the offset in force just before the transition: the offset
	// of the same wall time one day earlier is stable (transitions are at
	// least a day apart), and the instant it denotes lies before the gap.
	dayBefore := time.Date(y, m, d-1, hh, mm, ss, 0, loc)
	_, off := dayBefore.Zone()
	return time.Date(y, m, d, hh, mm, ss, 0, time.FixedZone("", off)).In(loc)
}

type hms struct{ hour, min, sec int }

// timeOfDayTuples returns the allowed (hour, minute, second) combinations. When
// none of BYHOUR/BYMINUTE/BYSECOND are present the single DTSTART time of day
// is used. When any is present, missing parts default to the DTSTART
// component.
func timeOfDayTuples(r *RecurrenceRule, dtstart time.Time) []hms {
	hours := r.ByHour
	mins := r.ByMinute
	secs := r.BySecond
	if hours == nil && mins == nil && secs == nil {
		return []hms{{dtstart.Hour(), dtstart.Minute(), dtstart.Second()}}
	}
	if hours == nil {
		hours = []int{dtstart.Hour()}
	}
	if mins == nil {
		mins = []int{dtstart.Minute()}
	}
	if secs == nil {
		secs = []int{dtstart.Second()}
	}
	out := make([]hms, 0, len(hours)*len(mins)*len(secs))
	for _, h := range hours {
		for _, mi := range mins {
			for _, s := range secs {
				out = append(out, hms{h, mi, s})
			}
		}
	}
	return out
}

// enumerateSubDay generates candidates for one SECONDLY/MINUTELY/HOURLY
// period. Without any BY-* limit/expansion parts the single period anchor
// (aligned to the FREQ field, carrying finer DTSTART components) is emitted.
// When finer BY parts exist they expand within the period; BYDAY/BYHOUR widen
// the scope to the containing day and are applied as limits.
func (it *ruleIterator) enumerateSubDay(period time.Time) ([]time.Time, error) {
	r := it.rule
	loc := it.dtstart.Location()

	// No expansion parts: one candidate at the period anchor.
	if r.BySecond == nil && r.ByMinute == nil && r.ByHour == nil &&
		len(r.ByDay) == 0 && len(r.ByMonthDay) == 0 && len(r.ByMonth) == 0 {
		return []time.Time{period}, nil
	}

	// Finer parts present: enumerate the appropriate domain for the period.
	switch r.Freq {
	case FrequencyHourly:
		// BYMINUTE/BYSECOND expand within this hour; missing parts default to
		// the DTSTART component. BYHOUR limits which hours (periods) qualify.
		if r.ByHour != nil && !containsInt(r.ByHour, period.Hour()) {
			return nil, nil
		}
		mins := orIntList(r.ByMinute, []int{it.dtstart.Minute()})
		secs := orIntList(r.BySecond, []int{it.dtstart.Second()})
		out := make([]time.Time, 0, len(mins)*len(secs))
		y, m, d := period.Date()
		for _, mi := range mins {
			for _, ss := range secs {
				out = append(out, normalizeWall(y, m, d, period.Hour(), mi, ss, loc))
			}
		}
		return out, nil
	case FrequencyMinutely:
		if r.ByHour != nil && !containsInt(r.ByHour, period.Hour()) {
			return nil, nil
		}
		// BYSECOND expands within the minute; BYMINUTE limits.
		if r.ByMinute != nil && !containsInt(r.ByMinute, period.Minute()) {
			return nil, nil
		}
		secs := orIntList(r.BySecond, []int{it.dtstart.Second()})
		out := make([]time.Time, 0, len(secs))
		y, m, d := period.Date()
		for _, ss := range secs {
			out = append(out, normalizeWall(y, m, d, period.Hour(), period.Minute(), ss, loc))
		}
		return out, nil
	case FrequencySecondly:
		if r.ByHour != nil && !containsInt(r.ByHour, period.Hour()) {
			return nil, nil
		}
		if r.ByMinute != nil && !containsInt(r.ByMinute, period.Minute()) {
			return nil, nil
		}
		if r.BySecond != nil && !containsInt(r.BySecond, period.Second()) {
			return nil, nil
		}
		return []time.Time{period}, nil
	}
	return nil, nil
}

func orIntList(v, def []int) []int {
	if v == nil {
		return def
	}
	return v
}

// enumerateSimpleDays expands a single day (sub-daily frequencies) or the
// INTERVAL-expanded day range for DAILY, then applies the day-level BY rules.
func (it *ruleIterator) enumerateSimpleDays(period time.Time) ([]time.Time, error) {
	r := it.rule
	var days []time.Time
	switch r.Freq {
	case FrequencySecondly, FrequencyMinutely, FrequencyHourly:
		days = []time.Time{period}
	case FrequencyDaily:
		days = []time.Time{period}
	default:
		days = []time.Time{period}
	}
	// With sub-daily or daily frequencies, BYDAY/BYMONTHDAY/BYYEARDAY and
	// BYMONTH act as limits and may select additional days relative to the
	// period day.
	if hasDayExpansion(r) {
		days = it.expandDayParts(period, r.Freq)
	}
	return filterDays(days, dayFilter{
		months: r.ByMonth,
		days:   r.ByMonthDay,
		dow:    r.ByDay,
	}), nil
}

func hasDayExpansion(r *RecurrenceRule) bool {
	return len(r.ByMonth) > 0 || len(r.ByMonthDay) > 0 || len(r.ByYearDay) > 0 || len(r.ByDay) > 0
}

// enumerateWeeklyDays applies BYDAY to the week containing period. Without
// BYDAY the rule generates the weekday of DTSTART.
func (it *ruleIterator) enumerateWeeklyDays(period time.Time) ([]time.Time, error) {
	r := it.rule
	days := make([]time.Time, 0, 7)
	var wanted []Weekday
	if len(r.ByDay) > 0 {
		for _, wd := range r.ByDay {
			wanted = append(wanted, wd.Day)
		}
	} else {
		wanted = []Weekday{weekdayToICS(it.dtstart.Weekday())}
	}
	allowed := map[time.Weekday]bool{}
	for _, w := range wanted {
		allowed[weekdayToGo(w)] = true
	}
	y0, m0, d0 := period.Date()
	for i := 0; i < 7; i++ {
		day := time.Date(y0, m0, d0+i, 0, 0, 0, 0, period.Location())
		if !allowed[day.Weekday()] {
			continue
		}
		if len(r.ByMonth) > 0 && !containsInt(r.ByMonth, int(day.Month())) {
			continue
		}
		days = append(days, day)
	}
	return days, nil
}

// enumerateMonthlyDays handles MONTHLY with BYMONTHDAY and/or BYDAY (ordinal
// and plain), plus BYMONTH expansion to extra months.
func (it *ruleIterator) enumerateMonthlyDays(period time.Time) ([]time.Time, error) {
	r := it.rule
	// For MONTHLY, BYMONTH is a limit: each period covers exactly one month,
	// so a month not named by BYMONTH yields nothing. Other months are covered
	// by their own periods.
	if len(r.ByMonth) > 0 && !containsInt(r.ByMonth, int(period.Month())) {
		return nil, nil
	}
	return it.daysInScopeMonth(period, false), nil
}

// enumerateYearlyDays handles YEARLY. With no day parts it generates the
// DTSTART month/day; BYMONTH+BYMONTHDAY/BYDAY and BYYEARDAY/BYWEEKNO expand the
// year.
func (it *ruleIterator) enumerateYearlyDays(period time.Time) ([]time.Time, error) {
	r := it.rule
	loc := it.dtstart.Location()
	year := period.Year()
	if len(r.ByYearDay) > 0 {
		return it.daysByYearDay(year, loc, r), nil
	}
	if len(r.ByWeekNo) > 0 {
		return it.daysByWeekNo(year, loc, r), nil
	}
	months := expandMonths(year, int(period.Month()), r.ByMonth, period.Location(), loc)
	if !hasDayExpansion(r) && len(r.ByMonth) == 0 {
		_, sm, sd := it.dtstart.Date()
		// A DTSTART day that does not exist in a given year (e.g. Feb 29 in a
		// common year) yields no occurrence that year; it is not normalized.
		if daysInMonth(year, sm) < sd {
			return nil, nil
		}
		return []time.Time{time.Date(year, sm, sd, 0, 0, 0, 0, loc)}, nil
	}
	if !hasDayExpansion(r) {
		// BYMONTH only: day of month comes from DTSTART.
		_, _, sd := it.dtstart.Date()
		var out []time.Time
		for _, ms := range months {
			if daysInMonth(ms.Year(), ms.Month()) >= sd {
				out = append(out, time.Date(ms.Year(), ms.Month(), sd, 0, 0, 0, 0, loc))
			}
		}
		return out, nil
	}
	if len(r.ByMonth) == 0 && containsOrdinal(r.ByDay) {
		// Ordinal BYDAY without BYMONTH is scoped to the whole year.
		days := it.yearScopedBydayDays(year)
		if len(r.ByMonthDay) > 0 {
			days = filterDays(days, dayFilter{days: r.ByMonthDay})
		}
		return days, nil
	}
	var days []time.Time
	for _, mStart := range months {
		days = append(days, it.daysInScopeMonth(mStart, false)...)
	}
	return days, nil
}

// yearScopedBydayDays unions year-ordinal weekday selections (e.g. 20MO) with
// every plain weekday named by BYDAY.
func (it *ruleIterator) yearScopedBydayDays(year int) []time.Time {
	r := it.rule
	loc := it.dtstart.Location()
	seen := map[string]bool{}
	var out []time.Time
	add := func(day time.Time) {
		key := day.Format("20060102")
		if !seen[key] {
			seen[key] = true
			out = append(out, day)
		}
	}
	for _, day := range it.ordinalBydayYear(year, loc, r.ByDay) {
		add(day)
	}
	hasPlain := false
	for _, wdn := range r.ByDay {
		if wdn.OrdWeek == 0 {
			hasPlain = true
		}
	}
	if hasPlain {
		jan1 := time.Date(year, 1, 1, 0, 0, 0, 0, loc)
		for i := 0; i < daysInYear(year); i++ {
			day := jan1.AddDate(0, 0, i)
			if plainByDayMatches(r.ByDay, day) {
				add(day)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// daysInScopeMonth enumerates the days of the month given by monthStart that
// satisfy BYMONTHDAY and/or BYDAY. When neither is present the DTSTART day of
// month is used. yearlyWithoutByMonth enables year-scoped ordinal BYDAY
// (e.g. FREQ=YEARLY;BYDAY=20MO).
func (it *ruleIterator) daysInScopeMonth(monthStart time.Time, yearlyWithoutByMonth bool) []time.Time {
	r := it.rule
	loc := it.dtstart.Location()
	y, m := monthStart.Year(), monthStart.Month()
	n := daysInMonth(y, m)
	_, _, startDom := it.dtstart.Date()
	ordinalOK := map[int]bool{}
	if containsOrdinal(r.ByDay) {
		ordinalOK = ordinalBydayMonth(y, m, n, loc, r.ByDay)
	}
	dayOK := map[int]bool{}
	switch {
	case len(r.ByMonthDay) > 0:
		for _, md := range r.ByMonthDay {
			d := md
			if d < 0 {
				d = n + d + 1
			}
			if d >= 1 && d <= n {
				dayOK[d] = true
			}
		}
	case len(ordinalOK) > 0:
		dayOK = ordinalOK
	case len(r.ByDay) > 0:
		for _, wdn := range r.ByDay {
			for d := 1; d <= n; d++ {
				if time.Date(y, m, d, 0, 0, 0, 0, loc).Weekday() == weekdayToGo(wdn.Day) {
					dayOK[d] = true
				}
			}
		}
	default:
		if startDom <= n {
			dayOK[startDom] = true
		}
	}
	_ = yearlyWithoutByMonth
	var out []time.Time
	for d := 1; d <= n; d++ {
		if !dayOK[d] {
			continue
		}
		day := time.Date(y, m, d, 0, 0, 0, 0, loc)
		if !plainByDayMatches(r.ByDay, day) {
			continue
		}
		if len(ordinalOK) > 0 && !ordinalOK[d] {
			continue
		}
		out = append(out, day)
	}
	return out
}

// expandDayParts handles the rare case of day expansion attached to a
// sub-daily/DAILY frequency: BYDAY stays within the period day unless BYMONTH
// or BYMONTHDAY/BYYEARDAY widen the scope (then the surrounding month/year is
// enumerated, which is safe because only matching periods are emitted).
func (it *ruleIterator) expandDayParts(period time.Time, freq Frequency) []time.Time {
	r := it.rule
	loc := it.dtstart.Location()
	if len(r.ByYearDay) > 0 {
		return it.daysByYearDay(period.Year(), loc, r)
	}
	y, m, _ := period.Date()
	if len(r.ByMonthDay) > 0 || len(r.ByMonth) > 0 || containsOrdinal(r.ByDay) {
		months := expandMonths(y, int(m), r.ByMonth, period.Location(), loc)
		var days []time.Time
		for _, ms := range months {
			days = append(days, it.daysInScopeMonth(ms, false)...)
		}
		return days
	}
	// Plain BYDAY acts as a limit on the period day.
	return []time.Time{period}
}

// daysByYearDay enumerates BYYEARDAY values within the year, intersecting with
// BYMONTH and BYDAY when present.
func (it *ruleIterator) daysByYearDay(year int, loc *time.Location, r *RecurrenceRule) []time.Time {
	total := daysInYear(year)
	var days []time.Time
	for _, yd := range r.ByYearDay {
		d := yd
		if d < 0 {
			d = total + d + 1
		}
		if d < 1 || d > total {
			continue
		}
		day := time.Date(year, 1, 1, 0, 0, 0, 0, loc).AddDate(0, 0, d-1)
		if len(r.ByMonth) > 0 && !containsInt(r.ByMonth, int(day.Month())) {
			continue
		}
		if !plainByDayMatches(r.ByDay, day) {
			continue
		}
		days = append(days, day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	return days
}

// daysByWeekNo enumerates ISO-style weeks selected by BYWEEKNO, intersected
// with BYDAY (defaulting to the DTSTART weekday) and BYMONTH (which must be
// absent per validation).
func (it *ruleIterator) daysByWeekNo(year int, loc *time.Location, r *RecurrenceRule) []time.Time {
	total := weeksInYear(year, it.wkst)
	var wanted map[time.Weekday]bool
	if len(r.ByDay) > 0 {
		wanted = map[time.Weekday]bool{}
		for _, wdn := range r.ByDay {
			if wdn.OrdWeek != 0 {
				continue
			}
			wanted[weekdayToGo(wdn.Day)] = true
		}
	} else {
		wanted = map[time.Weekday]bool{it.dtstart.Weekday(): true}
	}
	firstWeekStart := isoLikeFirstWeekStart(year, it.wkst, loc)
	var days []time.Time
	for _, wn := range r.ByWeekNo {
		w := wn
		if w < 0 {
			w = total + w + 1
		}
		if w < 1 || w > total {
			continue
		}
		for d := 0; d < 7; d++ {
			day := firstWeekStart.AddDate(0, 0, (w-1)*7+d)
			if day.Year() != year {
				continue
			}
			if !wanted[day.Weekday()] {
				continue
			}
			days = append(days, day)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	return days
}

// isoLikeFirstWeekStart returns midnight on the WKST day on which week 1
// begins: the WKST of the week containing January 4th (the week with at least
// four days in the new year).
func isoLikeFirstWeekStart(year int, wkst time.Weekday, loc *time.Location) time.Time {
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, loc)
	diff := (int(jan4.Weekday()) - int(wkst) + 7) % 7
	return jan4.AddDate(0, 0, -diff)
}

func weeksInYear(year int, wkst time.Weekday) int {
	loc := time.UTC
	start := isoLikeFirstWeekStart(year, wkst, loc)
	next := isoLikeFirstWeekStart(year+1, wkst, loc)
	return int(next.Sub(start).Hours() / 24 / 7)
}

// ordinalBydayMonth resolves ordinal BYDAY entries (e.g. 2MO, -1FR) against a
// single month. Non-ordinal entries select every matching weekday.
func ordinalBydayMonth(y int, m time.Month, n int, loc *time.Location, byday []WeekdayNum) map[int]bool {
	out := map[int]bool{}
	for _, wdn := range byday {
		goWd := weekdayToGo(wdn.Day)
		if wdn.OrdWeek == 0 {
			for d := 1; d <= n; d++ {
				if time.Date(y, m, d, 0, 0, 0, 0, loc).Weekday() == goWd {
					out[d] = true
				}
			}
			continue
		}
		ord := wdn.OrdWeek
		if ord > 0 {
			count := 0
			for d := 1; d <= n; d++ {
				if time.Date(y, m, d, 0, 0, 0, 0, loc).Weekday() == goWd {
					count++
					if count == ord {
						out[d] = true
						break
					}
				}
			}
		} else {
			count := 0
			target := -ord
			for d := n; d >= 1; d-- {
				if time.Date(y, m, d, 0, 0, 0, 0, loc).Weekday() == goWd {
					count++
					if count == target {
						out[d] = true
						break
					}
				}
			}
		}
	}
	return out
}

// ordinalBydayYear resolves ordinal BYDAY against the whole year for
// FREQ=YEARLY without BYMONTH (RFC 5545: ordinal counts across the year).
func (it *ruleIterator) ordinalBydayYear(year int, loc *time.Location, byday []WeekdayNum) []time.Time {
	total := daysInYear(year)
	jan1 := time.Date(year, 1, 1, 0, 0, 0, 0, loc)
	byDay := make(map[time.Weekday][]int)
	for _, wdn := range byday {
		goWd := weekdayToGo(wdn.Day)
		if wdn.OrdWeek == 0 {
			continue
		}
		byDay[goWd] = append(byDay[goWd], wdn.OrdWeek)
	}
	seen := map[string]bool{}
	var out []time.Time
	for wd, ords := range byDay {
		for _, ord := range ords {
			if ord > 0 {
				count := 0
				for i := 0; i < total; i++ {
					day := jan1.AddDate(0, 0, i)
					if day.Weekday() == wd {
						count++
						if count == ord {
							if !seen[day.Format("20060102")] {
								seen[day.Format("20060102")] = true
								out = append(out, day)
							}
							break
						}
					}
				}
			} else {
				count := 0
				target := -ord
				for i := total - 1; i >= 0; i-- {
					day := jan1.AddDate(0, 0, i)
					if day.Weekday() == wd {
						count++
						if count == target {
							if !seen[day.Format("20060102")] {
								seen[day.Format("20060102")] = true
								out = append(out, day)
							}
							break
						}
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// applyBySetPos narrows a period's candidate set to positions within that
// period (1-based; negative counts from the end). Candidates are assumed
// already generated in ascending order; sorting happens afterwards.
func applyBySetPos(r *RecurrenceRule, period time.Time, freq Frequency, cands []time.Time) []time.Time {
	if len(r.BySetPos) == 0 {
		return cands
	}
	n := len(cands)
	keep := make([]bool, n)
	for _, pos := range r.BySetPos {
		idx := pos
		if idx < 0 {
			idx = n + idx + 1
		} else {
			idx = pos
		}
		idx--
		if idx >= 0 && idx < n {
			keep[idx] = true
		}
	}
	out := cands[:0]
	for i, t := range cands {
		if keep[i] {
			out = append(out, t)
		}
	}
	return out
}

type dayFilter struct {
	months []int
	days   []int
	dow    []WeekdayNum
}

func filterDays(days []time.Time, f dayFilter) []time.Time {
	var out []time.Time
	for _, day := range days {
		if len(f.months) > 0 && !containsInt(f.months, int(day.Month())) {
			continue
		}
		ok := true
		if len(f.days) > 0 {
			ok = false
			_, _, dom := day.Date()
			n := daysInMonth(day.Year(), day.Month())
			for _, md := range f.days {
				d := md
				if d < 0 {
					d = n + d + 1
				}
				if d == dom {
					ok = true
					break
				}
			}
		}
		if ok && !plainByDayMatches(f.dow, day) {
			ok = false
		}
		if ok {
			out = append(out, day)
		}
	}
	return out
}

// plainByDayMatches applies the non-ordinal BYDAY entries as a weekday filter.
// When all entries have ordinals there is no plain filter (ordinals are
// resolved during day enumeration).
func plainByDayMatches(byday []WeekdayNum, day time.Time) bool {
	hasPlain := false
	for _, wdn := range byday {
		if wdn.OrdWeek != 0 {
			continue
		}
		hasPlain = true
		if day.Weekday() == weekdayToGo(wdn.Day) {
			return true
		}
	}
	if !hasPlain {
		return true
	}
	return false
}

func containsOrdinal(byday []WeekdayNum) bool {
	for _, wdn := range byday {
		if wdn.OrdWeek != 0 {
			return true
		}
	}
	return false
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// expandMonths returns midnight-on-the-first for every month named by BYMONTH
// that belongs to the current scope. For MONTHLY periods only the period's own
// month is eligible (BYMONTH acts as a limit); for YEARLY periods all twelve
// months are eligible (BYMONTH expands).
func expandMonths(year, periodMonth int, byMonth []int, periodLoc, loc *time.Location) []time.Time {
	if len(byMonth) == 0 {
		return []time.Time{time.Date(year, time.Month(periodMonth), 1, 0, 0, 0, 0, loc)}
	}
	months := append([]int(nil), byMonth...)
	sort.Ints(months)
	var out []time.Time
	for _, m := range months {
		out = append(out, time.Date(year, time.Month(m), 1, 0, 0, 0, 0, loc))
	}
	return out
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func daysInYear(year int) int {
	if time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC).YearDay() == 366 {
		return 366
	}
	return 365
}
