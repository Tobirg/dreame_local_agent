package main

import (
	"testing"
)

func TestDNDPeriods(t *testing.T) {
	got := parseDNDPeriods(`[{"id":0,"en":false,"st":"22:00","et":"08:00","wk":127,"ss":0},{"id":2,"en":true,"st":"13:00","et":"15:00","wk":65}]`)
	if len(got) != 2 || got[0].Days != "1111111" || got[1].Days != "1000001" || !got[1].Enabled || got[1].ID != 2 {
		t.Fatalf("%+v", got)
	}
	a := testAgent(t)
	a.dev.details = map[string]any{"dnd_periods": got}
	on := true
	method, params, err := a.dndPeriodCommand(commandParams{Name: "set_dnd_period", Enabled: &on, Start: "13:00", End: "15:00", Days: "0111110"}, "1")
	if err != nil || method != "set_properties" {
		t.Fatal(method, err)
	}
	// First free id is 1; Monday-Friday is 0111110 = 62.
	if v := params.([]any)[0].(map[string]any)["value"]; v != `[{"en":true,"et":"15:00","id":1,"st":"13:00","wk":62}]` {
		t.Fatal(v)
	}
	if _, _, err := a.dndPeriodCommand(commandParams{Name: "set_dnd_period", Enabled: &on, Start: "13:00", End: "13:00", Days: "1111111"}, "1"); err == nil {
		t.Fatal("equal start and end must fail")
	}
	id := 2
	_, params, err = a.dndPeriodCommand(commandParams{Name: "delete_dnd_period", ID: &id}, "1")
	if err != nil || params.(map[string]any)["in"].([]any)[0].(map[string]any)["value"] != "2" {
		t.Fatal(params, err)
	}
}
