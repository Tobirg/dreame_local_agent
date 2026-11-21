package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseDetails(t *testing.T) {
	// Values as answered by r2505 4.3.9_2021; 18/x (mop pad) is not answered there.
	raw := json.RawMessage(`[{"siid":9,"piid":1,"value":158,"code":0},{"siid":9,"piid":2,"value":52,"code":0},
		{"siid":11,"piid":1,"value":6,"code":0},{"siid":11,"piid":2,"value":8,"code":0},{"siid":18,"piid":1,"code":-4001},
		{"siid":5,"piid":1,"value":false,"code":0},{"siid":5,"piid":2,"value":"22:00","code":0},{"siid":5,"piid":3,"value":"08:00","code":0},
		{"siid":12,"piid":2,"value":308,"code":0},{"siid":12,"piid":3,"value":22,"code":0},{"siid":12,"piid":4,"value":264,"code":0}]`)
	got, _ := json.Marshal(parseDetails(raw))
	want := `{"consumables":{"filter":{"hours":8,"left":6},"main_brush":{"hours":158,"left":52}},"dnd":{"enabled":false,"end":"08:00","start":"22:00"},"stats":{"area":264,"cleanings":22,"minutes":308}}`
	if string(got) != want {
		t.Fatal(string(got))
	}
}

func TestDetailCommands(t *testing.T) {
	a := testAgent(t)
	on := true
	if _, _, err := a.detailCommand(commandParams{Name: "set_dnd", Enabled: &on, Start: "25:00", End: "08:00"}, "1"); err == nil {
		t.Fatal("bad time accepted")
	}
	method, params, err := a.detailCommand(commandParams{Name: "set_dnd", Enabled: &on, Start: "22:30", End: "07:00"}, "1")
	if err != nil || method != "set_properties" || len(params.([]any)) != 3 {
		t.Fatal(method, params, err)
	}
	// A consumable the robot never reported cannot be reset.
	if _, _, err := a.detailCommand(commandParams{Name: "reset_consumable", Consumable: "filter"}, "1"); err == nil || !strings.Contains(err.Error(), "does not report") {
		t.Fatal(err)
	}
	a.dev.details = parseDetails(json.RawMessage(`[{"siid":11,"piid":1,"value":6,"code":0}]`))
	method, params, err = a.detailCommand(commandParams{Name: "reset_consumable", Consumable: "filter"}, "1")
	if err != nil || method != "action" || !reflect.DeepEqual(params, map[string]any{"did": "1", "siid": 11, "aiid": 1, "in": []any{}}) {
		t.Fatal(method, params, err)
	}
	if _, _, err := a.detailCommand(commandParams{Name: "reset_consumable", Consumable: "nope"}, "1"); err == nil {
		t.Fatal("unknown consumable accepted")
	}
	if _, err := a.zoneInput(commandParams{Zones: [][]int{{0, 0, 1000, 1000}}, Repeats: 1, Suction: 1, Water: 2}); err == nil {
		t.Fatal("zone accepted without a saved map")
	}
	if _, err := a.zoneInput(commandParams{Zones: [][]int{{0, 0, 1000}}, Repeats: 4}); err == nil {
		t.Fatal("bad zone accepted")
	}
	// 3x3 cells of 500 mm from origin (-100,-200): x -100..1400, y -200..1300.
	maps, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"id": 0, "map": syntheticFrame(t, 2, 500)}}})
	writeFile(t, a, "/data/log/map_info.bin", string(maps))
	zone := func(z ...int) ([]any, error) {
		return a.zoneInput(commandParams{Zones: [][]int{z}, Repeats: 2, Suction: 1, Water: 3})
	}
	in, err := zone(900, 800, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(in); string(got) != `[{"piid":1,"value":19},{"piid":10,"value":"{\"areas\":[[0,0,900,800,2,1,3]]}"}]` {
		t.Fatal(string(got))
	}
	if _, err := zone(0, 0, 2000, 800); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatal("zone outside the map accepted", err)
	}
	if _, err := zone(0, 0, 100, 800); err == nil {
		t.Fatal("tiny zone accepted")
	}
}
