// Cleaning history. The firmware sends finished-cleaning records to the cloud and keeps
// none locally (clean_log.db stays empty on r2505), so the agent records runs itself:
// start and end from the activity it polls, time and area as the change of the robot's
// lifetime totals (12/2 minutes, 12/4 m²) between the start and the end of the run.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	historyFile = "/data/dreame-local/history.json"
	maxHistory  = 200
)

type cleanRun struct {
	Start   time.Time  `json:"start"`
	End     *time.Time `json:"end"`
	Mode    string     `json:"mode"`              // status at start: cleaning, room_cleaning, zone_cleaning, mapping…
	Result  string     `json:"result"`            // activity at the end: docked, idle or error
	Error   string     `json:"error,omitempty"`   // error name when the run ended in an error
	Minutes *float64   `json:"minutes,omitempty"` // missing when the robot did not report its totals
	Area    *float64   `json:"area,omitempty"`

	startStats map[string]any
	finishing  bool
}

func number(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// trackRun runs with a.dev.mu held after each successful poll; freshTotals says the
// poll also re-read the lifetime totals.
// shortcut: a run in progress is lost if the agent restarts; persist a.dev.run if that matters.
func (a *agent) trackRun(state map[string]any, freshTotals bool) {
	activity, _ := state["activity"].(string)
	stats, _ := state["stats"].(map[string]any)
	now := time.Now().UTC()
	r := a.dev.run
	switch {
	case r == nil && activity == "cleaning":
		mode, _ := state["status_name"].(string)
		a.dev.run = &cleanRun{Start: now, Mode: mode, startStats: stats}
	case r != nil && !r.finishing && activity != "cleaning" && activity != "paused" && activity != "returning":
		r.End, r.Result, r.finishing = &now, activity, true
		if activity == "error" {
			r.Error, _ = state["error_name"].(string)
		}
		a.dev.detailsAt = time.Time{} // read the totals on the next poll
	case r != nil && r.finishing && freshTotals:
		for key, field := range map[string]**float64{"minutes": &r.Minutes, "area": &r.Area} {
			before, ok1 := number(r.startStats[key])
			after, ok2 := number(stats[key])
			if ok1 && ok2 && after >= before {
				delta := after - before
				*field = &delta
			}
		}
		a.dev.run = nil
		_ = a.appendHistory(*r)
		go a.mapView() // keeps the run's obstacles before the firmware's log rotates them away
	}
}

func (a *agent) readHistory() []cleanRun {
	runs := []cleanRun{}
	if b, err := readLimited(a.path(historyFile), 1<<20); err == nil {
		_ = json.Unmarshal(b, &runs)
	}
	return runs
}

// appendHistory keeps the newest maxHistory runs, newest first.
func (a *agent) appendHistory(r cleanRun) error {
	runs := append([]cleanRun{r}, a.readHistory()...)
	if len(runs) > maxHistory {
		runs = runs[:maxHistory]
	}
	b, err := json.Marshal(runs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path(historyFile)), 0700); err != nil {
		return err
	}
	return atomicPrivate(a.path(historyFile), b)
}
