package ics

// ExpandCalendar groups the calendar's VEVENTs by UID, attaches RECURRENCE-ID
// overrides to their master events, and expands each master with the same
// options.
//
// Results are returned in master document order. A RECURRENCE-ID event without
// a matching master is expanded as a standalone single-instance event so that
// no data is silently dropped.
func ExpandCalendar(cal *Calendar, options ...ExpandOption) ([]*ExpansionResult, error) {
	if cal == nil {
		return nil, expansionError("calendar", errCalendarNil)
	}

	events := cal.Events()
	masterIndex := map[string]int{}
	type group struct {
		master    *VEvent
		overrides []*VEvent
		order     int
	}
	groups := map[string]*group{}
	var order []string

	var orphans []*VEvent
	for _, ev := range events {
		if ev.GetProperty(ComponentPropertyRecurrenceId) != nil {
			uid := ev.Id()
			if g, ok := groups[uid]; ok {
				g.overrides = append(g.overrides, ev)
			} else {
				orphans = append(orphans, ev)
			}
			continue
		}
		uid := ev.Id()
		g := &group{master: ev, order: len(order)}
		groups[uid] = g
		masterIndex[uid] = len(order)
		order = append(order, uid)
	}

	// Attach late-arriving overrides (RECURRENCE-ID seen before its master) by
	// doing a second pass over the orphan list against known masters.
	var standalone []*VEvent
	for _, ov := range orphans {
		if g, ok := groups[ov.Id()]; ok {
			g.overrides = append(g.overrides, ov)
		} else {
			standalone = append(standalone, ov)
		}
	}

	results := make([]*ExpansionResult, 0, len(order)+len(standalone))
	for _, uid := range order {
		g := groups[uid]
		opts := append([]ExpandOption{WithOverrides(g.overrides...)}, options...)
		res, err := ExpandEvent(g.master, opts...)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}
	for _, ev := range standalone {
		res, err := ExpandEvent(ev, options...)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}

	return results, nil
}
