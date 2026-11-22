package main

import (
	"encoding/json"
	"testing"
)

func TestTrackRun(t *testing.T) {
	a := testAgent(t)
	state := func(activity, status string, minutes, area int) map[string]any {
		return map[string]any{"activity": activity, "status_name": status, "error_name": "bin_full",
			"stats": map[string]any{"minutes": json.Number(itoa(minutes)), "area": json.Number(itoa(area))}}
	}
	poll := func(s map[string]any, fresh bool) {
		a.dev.mu.Lock()
		a.trackRun(s, fresh)
		a.dev.mu.Unlock()
	}
	poll(state("docked", "charging", 300, 260), true)
	if a.dev.run != nil {
		t.Fatal("run started while docked")
	}
	poll(state("cleaning", "room_cleaning", 300, 260), false)
	poll(state("paused", "paused", 300, 260), false)
	poll(state("returning", "returning", 300, 260), false)
	if len(a.readHistory()) != 0 {
		t.Fatal("run recorded before it ended")
	}
	poll(state("docked", "charging", 300, 260), false) // ended; totals not re-read yet
	if !a.dev.detailsAt.IsZero() || len(a.readHistory()) != 0 {
		t.Fatal("end must ask for fresh totals before recording")
	}
	poll(state("docked", "charging", 312, 271), true)
	runs := a.readHistory()
	if len(runs) != 1 || runs[0].Mode != "room_cleaning" || runs[0].Result != "docked" || runs[0].Error != "" ||
		*runs[0].Minutes != 12 || *runs[0].Area != 11 || runs[0].End == nil || a.dev.run != nil {
		t.Fatalf("%+v", runs)
	}
	// A run that ends in an error keeps the error name; newest first.
	poll(state("cleaning", "cleaning", 312, 271), false)
	poll(state("error", "error", 312, 271), false)
	poll(state("error", "error", 315, 273), true)
	if runs = a.readHistory(); len(runs) != 2 || runs[0].Result != "error" || runs[0].Error != "bin_full" || *runs[0].Area != 2 {
		t.Fatalf("%+v", runs[0])
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
