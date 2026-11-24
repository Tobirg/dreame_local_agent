package main

import (
	"encoding/json"
	"testing"
)

func TestQuickCommands(t *testing.T) {
	got := parseQuickCommands(`[{"id":32,"name":"0JrRg9GF0L3Rjw==","mapId":2},{"name":"bad"}]`)
	if len(got) != 1 || got[0]["id"] != 32 || got[0]["name"] != "Кухня" {
		t.Fatalf("%+v", got)
	}
	id := 32
	method, params, err := (&agent{}).quickCommandCommand(commandParams{Name: "delete_quick_command", ID: &id}, "1")
	in := params.(map[string]any)["in"].([]any)[0].(map[string]any)
	var v struct {
		Cmd    string         `json:"cmd"`
		Params map[string]any `json:"params"`
	}
	if err != nil || method != "action" || params.(map[string]any)["aiid"] != 8 || json.Unmarshal([]byte(in["value"].(string)), &v) != nil || v.Cmd != "EDIT_COMMAND" || v.Params["type"] != 4.0 || v.Params["id"] != 32.0 {
		t.Fatal(method, params, err)
	}
	if _, _, err := (&agent{}).quickCommandCommand(commandParams{Name: "save_quick_command", Rooms: []int{1}}, "1"); err == nil {
		t.Fatal("a name is required")
	}
}
