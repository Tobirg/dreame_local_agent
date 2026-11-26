package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRoomTypes(t *testing.T) {
	a := testAgent(t)
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	// Room 1: bedroom; room 2: own name "Детская" (base64, as the app saves it).
	raw = append(raw[:36], []byte(`{"origin":[-100,-200],"seg_inf":{"1":{"type":2,"index":0},"2":{"type":0,"index":0,"name":"0JTQtdGC0YHQutCw0Y8="}}}`)...)
	body, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"map": encodeTestFrame(t, raw)}}})
	if err := os.WriteFile(a.path("/data/log/map_info.bin"), body, 0600); err != nil {
		t.Fatal(err)
	}
	view, err := a.mapView()
	if err != nil || len(view.Rooms) != 2 || view.Rooms[0].Name != "Спальня" || view.Rooms[0].Type != 2 || view.Rooms[1].Name != "Детская" {
		t.Fatalf("%v %+v", err, view.Rooms)
	}
	edit := func(rooms string) string {
		t.Helper()
		patch, err := a.roomTypesPatch(json.RawMessage(`{"map_id":2,"revision":"` + view.Revision + `","rooms":` + rooms + `}`))
		if err != nil {
			t.Fatal(rooms, err)
		}
		b, _ := json.Marshal(patch)
		return string(b)
	}
	if got := edit(`[{"id":2,"type":2}]`); got != `{"mapid":2,"nsr":{"2":{"index":1,"type":2}}}` {
		t.Error("second bedroom gets index 1:", got)
	}
	if got := edit(`[{"id":1,"type":6},{"id":2,"type":2}]`); got != `{"mapid":2,"nsr":{"1":{"index":0,"type":6},"2":{"index":0,"type":2}}}` {
		t.Error(got)
	}
	if got := edit(`[{"id":1,"type":0,"name":" Кот "}]`); got != `{"mapid":2,"nsr":{"1":{"index":0,"name":"0JrQvtGC","type":0}}}` {
		t.Error(got)
	}
	for _, bad := range []string{`[]`, `[{"id":9,"type":1}]`, `[{"id":1,"type":16}]`, `[{"id":1,"type":0}]`, `[{"id":1,"type":1},{"id":1,"type":2}]`} {
		if _, err := a.roomTypesPatch(json.RawMessage(`{"map_id":2,"revision":"` + view.Revision + `","rooms":` + bad + `}`)); err == nil {
			t.Error("accepted", bad)
		}
	}
	if _, err := a.roomTypesPatch(json.RawMessage(`{"map_id":2,"revision":"old","rooms":[{"id":1,"type":1}]}`)); err == nil {
		t.Error("stale revision accepted")
	}
}
