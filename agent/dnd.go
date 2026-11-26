package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// Do-not-disturb periods of the new app (DNDTimeListPage, AddDNDTimePageNew): 5/4 holds a JSON
// list [{"id", "en", "st", "et", "wk", "ss"}]. "wk" is the seven Sunday-first day digits read as
// a binary number (127 = every day). Writing a one-item list adds the period or replaces the
// one with the same id, like the app; action 5/1 with 5/4 = id as a string deletes it.

type dndPeriod struct {
	ID      int    `json:"id"`
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Days    string `json:"days"`
}

func parseDNDPeriods(value any) []dndPeriod {
	s, _ := value.(string)
	var rows []struct {
		ID json.Number `json:"id"`
		En bool        `json:"en"`
		St string      `json:"st"`
		Et string      `json:"et"`
		Wk *int        `json:"wk"`
	}
	out := []dndPeriod{}
	if json.Unmarshal([]byte(s), &rows) != nil {
		return out
	}
	for _, r := range rows {
		id, err := strconv.Atoi(r.ID.String())
		if err != nil {
			continue
		}
		days := "1111111" // firmware without "wk" repeats every day
		if r.Wk != nil {
			days = fmt.Sprintf("%07b", *r.Wk&127)
		}
		out = append(out, dndPeriod{ID: id, Enabled: r.En, Start: r.St, End: r.Et, Days: days})
	}
	return out
}

func (a *agent) dndPeriodCommand(p commandParams, did string) (string, any, error) {
	if p.Name == "delete_dnd_period" {
		if p.ID == nil || *p.ID < 0 {
			return "", nil, errors.New("delete_dnd_period requires id")
		}
		return "action", map[string]any{"did": did, "siid": 5, "aiid": 1, "in": []any{map[string]any{"piid": 4, "value": strconv.Itoa(*p.ID)}}}, nil
	}
	if p.Enabled == nil || !clockRe.MatchString(p.Start) || !clockRe.MatchString(p.End) || p.Start == p.End || len(p.Days) != 7 || !daysRe.MatchString(p.Days) || p.Days == "0000000" {
		return "", nil, errors.New("set_dnd_period requires enabled, start/end HH:MM (different) and days as seven 0/1 digits from Sunday, at least one 1")
	}
	a.dev.mu.Lock()
	periods, _ := a.dev.details["dnd_periods"].([]dndPeriod)
	a.dev.mu.Unlock()
	id := 0
	if p.ID != nil {
		if id = *p.ID; id < 0 || id > 99 {
			return "", nil, errors.New("period id must be 0..99")
		}
	} else {
		if len(periods) >= 10 {
			return "", nil, errors.New("at most 10 periods")
		}
		used := map[int]bool{}
		for _, d := range periods {
			used[d.ID] = true
		}
		for used[id] {
			id++
		}
	}
	wk, _ := strconv.ParseInt(p.Days, 2, 8)
	value, _ := json.Marshal([]map[string]any{{"id": id, "en": *p.Enabled, "st": p.Start, "et": p.End, "wk": wk}})
	return "set_properties", []any{map[string]any{"did": did, "siid": 5, "piid": 4, "value": string(value)}}, nil
}
