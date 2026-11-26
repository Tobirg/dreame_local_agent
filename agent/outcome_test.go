package main

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

func TestCommandOutcome(t *testing.T) {
	a := testAgent(t)
	three := 3
	state := func(activity string, suction int) map[string]any {
		return map[string]any{"activity": activity, "suction": json.Number(strconv.Itoa(suction))}
	}
	a.noteCommand(commandParams{Name: "locate"})
	if c := a.snapshot().Command; c.Outcome != "accepted" || a.settleCommandLocked(state("docked", 1), time.Now()) {
		t.Fatal("nothing to watch", c)
	}
	a.noteCommand(commandParams{Name: "set_suction", Value: &three})
	held := a.snapshot().Command
	if held.Outcome != "pending" || a.settleCommandLocked(state("docked", 1), time.Now()) {
		t.Fatal("old suction must not settle it")
	}
	if !a.settleCommandLocked(state("docked", 3), time.Now()) || a.snapshot().Command.Outcome != "executed" || held.Outcome != "pending" {
		t.Fatal("suction 3 settles it without touching snapshots already handed out", a.snapshot().Command, held)
	}
	a.noteCommand(commandParams{Name: "start"})
	if a.settleCommandLocked(state("docked", 1), time.Now()) || !a.settleCommandLocked(state("docked", 1), time.Now().Add(commandConfirm+time.Second)) ||
		a.snapshot().Command.Outcome != "unknown" {
		t.Fatal("no cleaning within the window is unknown", a.snapshot().Command)
	}
	for name, s := range map[string]string{"stop": "idle", "dock": "returning", "pause": "paused", "clean_zone": "cleaning"} {
		if !commandEffect(commandParams{Name: name})(state(s, 1)) {
			t.Error(name, s)
		}
	}
	if commandEffect(commandParams{Name: "stop"})(state("cleaning", 1)) {
		t.Error("stop while cleaning")
	}
}
