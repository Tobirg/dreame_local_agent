package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStateMessages(t *testing.T) {
	n := func(v int) json.Number { return json.Number(itoa(v)) }
	part := func(left int) map[string]any { return map[string]any{"filter": map[string]any{"left": n(left)}} }
	now := time.Unix(0, 0).UTC()
	if stateMessages(nil, map[string]any{"error": n(8)}, now) != nil {
		t.Fatal("first poll must only set a baseline")
	}
	prev := map[string]any{"error": n(0), "activity": "cleaning", "status": n(18), "consumables": part(11)}
	next := map[string]any{"error": n(8), "error_name": "dust_bin_removed", "error_text": "official", "activity": "error", "status": n(12), "consumables": part(10)}
	got := stateMessages(prev, next, now)
	if len(got) != 3 || got[0].Type != "error" || got[0].Code != 8 || got[0].Text != "official" || got[1].Type != "consumable" || got[1].Name != "filter" || got[2].Type != "run" || got[2].Name != "error" {
		t.Fatalf("%+v", got)
	}
	// Same error, a consumable already low, docked from docked: nothing new.
	if got := stateMessages(next, next, now); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	selfTest := stateMessages(map[string]any{"status": n(6), "activity": "docked"}, map[string]any{"status": n(15), "activity": "docked"}, now)
	if len(selfTest) != 1 || selfTest[0].Type != "self_test" || selfTest[0].Code != 15 {
		t.Fatalf("%+v", selfTest)
	}

	a := testAgent(t)
	a.noteMessages(prev, next)
	a.noteMessages(map[string]any{"activity": "returning"}, map[string]any{"activity": "docked"})
	list := a.readMessages()
	if len(list) != 4 || list[0].Type != "run" || list[0].Name != "docked" || list[1].Type != "error" {
		t.Fatalf("%+v", list)
	}
}
