package main

import "testing"

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
