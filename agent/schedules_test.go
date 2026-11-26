package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestParseSchedules(t *testing.T) {
	got := parseSchedules("1-1-08:30-111110-1-3-0-0-0;2-0-21:00-0000000-0-3-0-0-" + "1180676,8452" + ";bad")
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got[0]["days"] != "0111110" || got[0]["enabled"] != true || got[0]["rooms"] != nil {
		t.Fatalf("first %v", got[0])
	}
	rooms, _ := got[1]["rooms"].([]int)
	if got[1]["enabled"] != false || got[1]["repeat"] != false || len(rooms) != 2 || rooms[0] != 4 || rooms[1] != 4 {
		t.Fatalf("second %v", got[1])
	}
	if len(parseSchedules("")) != 0 {
		t.Fatal("empty value must give no schedules")
	}
}

func TestEncodeTimingRoom(t *testing.T) {
	// room 4, 1 repeat, water 2, suction 2, mode 1, as ProtocolUtil.encodeTimingCustom packs it.
	if v := encodeTimingRoom(4, 1, 2, 2, 1); v != 1<<20|2<<16|2<<12|1<<8|4 {
		t.Fatalf("got %d", v)
	}
}

func TestCameraPatrol(t *testing.T) {
	a := testAgent(t)
	maps, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"id": 0, "map": syntheticFrame(t, 2, 500)}}})
	writeFile(t, a, "/data/log/map_info.bin", string(maps))
	_, params, err := a.detailCommand(commandParams{Name: "go_to", Points: [][]int{{0, 0}}}, "1")
	in, _ := json.Marshal(params)
	if err != nil || string(in) != `{"aiid":1,"did":"1","in":[{"piid":1,"value":23},{"piid":10,"value":"{\"spoint\":[],\"tpoint\":[[0,0,0,0]]}"}],"siid":4}` {
		t.Fatal(string(in), err)
	}
	if _, _, err := a.detailCommand(commandParams{Name: "go_to", Points: [][]int{{0, 0}, {1, 1}}}, "1"); err == nil {
		t.Fatal("go_to with two points accepted")
	}
	if _, err := a.startPatrol(context.Background(), commandParams{Name: "patrol", Points: [][]int{{0, 0}, {90000, 0}}}, "1"); err == nil {
		t.Fatal("point off the map accepted")
	}
}

func TestScheduleQuickCommand(t *testing.T) {
	got := parseSchedules("3-1-09:00-1111111-1-2-0-8192-0")
	if len(got) != 1 || got[0]["quick_command"] != 32 {
		t.Fatalf("%+v", got)
	}
}
