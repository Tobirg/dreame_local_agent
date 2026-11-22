package main

import (
	"encoding/json"
	"testing"
)

func TestRoomSettings(t *testing.T) {
	a := testAgent(t)
	view := restrictionsFixture(t, a, `{"line":[],"rect":[],"mop":[]}`)
	edit := func(rooms string) (map[string]any, error) {
		return a.roomSettingsPatch(json.RawMessage(`{"map_id":2,"revision":"` + view.Revision + `","rooms":` + rooms + `}`))
	}
	if _, err := edit(`[{"id":1,"suction":1,"water":2,"repeats":1,"mode":2}]`); err == nil {
		t.Fatal("edit accepted without settings on the robot")
	}
	// As kept by r2505: [suction, water, repeats, order, mode, extra].
	writeFile(t, a, roomSettingsFile, `{"map_names":{},"clean_sets":{"2":{"1":[1,3,1,2,2,546],"2":[0,1,1,1,2,546]},"7":{"1":[3,3,3,1,0,0]}}}`)
	view, _ = a.mapView()
	if got, _ := json.Marshal(view.RoomSettings); string(got) != `{"1":[1,3,1,2,2,546],"2":[0,1,1,1,2,546]}` {
		t.Fatal(string(got))
	}
	patch, err := edit(`[{"id":1,"suction":3,"water":1,"repeats":2,"mode":0}]`)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(patch); string(got) != `{"customeClean":[[1,3,1,2,0,546]]}` {
		t.Fatal("extra field must be carried over:", string(got))
	}
	for _, bad := range []string{`[]`, `[{"id":9,"suction":1,"water":1,"repeats":1,"mode":0}]`, `[{"id":1,"suction":4,"water":1,"repeats":1,"mode":0}]`, `[{"id":1,"suction":1,"water":0,"repeats":1,"mode":0}]`, `[{"id":1,"suction":1,"water":1,"repeats":1,"mode":3}]`} {
		if _, err := edit(bad); err == nil {
			t.Error("accepted", bad)
		}
	}
}
