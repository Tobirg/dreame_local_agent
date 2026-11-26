package main

import (
	"slices"
	"time"
)

// commandOutcome is the last command the robot accepted and whether its state has shown the effect:
// "pending" while waiting, "executed" when it did, "unknown" when it did not within commandConfirm,
// "accepted" for commands with nothing to watch (locate, resets, settings, station actions).
type commandOutcome struct {
	Name    string    `json:"name"`
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"`
	done    func(map[string]any) bool
}

const commandConfirm = 30 * time.Second

// commandEffect says how the polled state shows that a command took effect; nil when it does not.
func commandEffect(p commandParams) func(map[string]any) bool {
	activity := func(want ...string) func(map[string]any) bool {
		return func(s map[string]any) bool { a, _ := s["activity"].(string); return slices.Contains(want, a) }
	}
	value := func(key string) func(map[string]any) bool {
		return func(s map[string]any) bool {
			v, ok := int64Of(s[key])
			return ok && p.Value != nil && v == int64(*p.Value)
		}
	}
	switch p.Name {
	case "start", "clean_rooms", "clean_zone", "clean_spot":
		return activity("cleaning")
	case "pause":
		return activity("paused")
	case "stop":
		return func(s map[string]any) bool {
			a, _ := s["activity"].(string)
			return a != "" && a != "cleaning" && a != "paused"
		}
	case "dock":
		return activity("returning", "docked")
	case "set_suction":
		return value("suction")
	case "set_volume":
		return value("volume")
	}
	return nil
}

// noteCommand records an accepted command for the state stream.
func (a *agent) noteCommand(p commandParams) {
	c := &commandOutcome{Name: p.Name, At: time.Now().UTC(), Outcome: "accepted", done: commandEffect(p)}
	if c.done != nil {
		c.Outcome = "pending"
	}
	a.dev.mu.Lock()
	defer a.dev.mu.Unlock()
	a.dev.snap.Command = c
	a.bumpLocked()
}

// settleCommandLocked moves a pending command to executed or unknown after a poll; true when it changed.
func (a *agent) settleCommandLocked(state map[string]any, now time.Time) bool {
	c := a.dev.snap.Command
	if c == nil || c.Outcome != "pending" {
		return false
	}
	n := *c // snapshots handed out share the old pointer, so replace instead of mutating
	switch {
	case c.done(state):
		n.Outcome = "executed"
	case now.Sub(c.At) > commandConfirm:
		n.Outcome = "unknown"
	default:
		return false
	}
	a.dev.snap.Command = &n
	return true
}
