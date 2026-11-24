package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The app's message list comes from the cloud; the agent keeps its own from state changes:
// a new robot error, a consumable at 10 % or worn out, a run ending, the station self-test.
// Texts are made by the panel from type and code; errors also carry the app's own text.

const (
	messagesFile = "/data/dreame-local/messages.json"
	maxMessages  = 100
)

type message struct {
	Time time.Time `json:"time"`
	Type string    `json:"type"` // error, consumable, run, self_test
	Code int64     `json:"code,omitempty"`
	Name string    `json:"name,omitempty"`
	Text string    `json:"text,omitempty"` // error: the Dreame app's text (error_text)
}

// stateMessages compares two polls; prev is nil on the first one, which only sets a baseline.
func stateMessages(prev, next map[string]any, now time.Time) []message {
	if prev == nil {
		return nil
	}
	out := []message{}
	if code, ok := int64Of(next["error"]); ok && code != 0 {
		if old, _ := int64Of(prev["error"]); old != code {
			name, _ := next["error_name"].(string)
			text, _ := next["error_text"].(string)
			out = append(out, message{now, "error", code, name, text})
		}
	}
	oldParts, _ := prev["consumables"].(map[string]any)
	parts, _ := next["consumables"].(map[string]any)
	for name, v := range parts {
		c, _ := v.(map[string]any)
		left, ok := int64Of(c["left"])
		before, had := int64(101), false
		if o, ok := oldParts[name].(map[string]any); ok {
			before, had = int64Of(o["left"])
		}
		if ok && had && (left <= 10 && before > 10 || left == 0 && before > 0) {
			out = append(out, message{now, "consumable", left, name, ""})
		}
	}
	was, _ := prev["activity"].(string)
	is, _ := next["activity"].(string)
	if was == "cleaning" && is != "cleaning" && is != "paused" && is != "returning" || was == "returning" && is == "docked" {
		out = append(out, message{now, "run", 0, is, ""})
	}
	oldStatus, _ := int64Of(prev["status"])
	status, _ := int64Of(next["status"])
	if (status == 15) != (oldStatus == 15) {
		out = append(out, message{now, "self_test", status, "", ""})
	}
	return out
}

func (a *agent) readMessages() []message {
	list := []message{}
	if b, err := readLimited(a.path(messagesFile), 1<<20); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	return list
}

func (a *agent) writeMessages(list []message) error {
	if len(list) > maxMessages {
		list = list[:maxMessages]
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path(messagesFile)), 0700); err != nil {
		return err
	}
	return atomicPrivate(a.path(messagesFile), b)
}

// noteMessages runs with a.dev.mu held, like trackRun; newest messages first.
func (a *agent) noteMessages(prev, next map[string]any) {
	if found := stateMessages(prev, next, time.Now().UTC()); len(found) > 0 {
		_ = a.writeMessages(append(found, a.readMessages()...))
	}
}
