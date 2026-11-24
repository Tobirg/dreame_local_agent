package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRoomFloors(t *testing.T) {
	a := testAgent(t)
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	// As the X50 saves them: room 1 tiles, room 2 planks along x.
	raw = append(raw[:36], []byte(`{"origin":[-100,-200],"seg_inf":{"1":{"type":6,"index":0,"material":2,"direction":0},"2":{"type":4,"index":0,"material":1,"direction":0}}}`)...)
	body, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"map": encodeTestFrame(t, raw)}}})
	if err := os.WriteFile(a.path("/data/log/map_info.bin"), body, 0600); err != nil {
		t.Fatal(err)
	}
	view, err := a.mapView()
	if err != nil || len(view.Rooms) != 2 || view.Rooms[0].Floor != 2 || view.Rooms[1].Floor != 4 {
		t.Fatalf("%v %+v", err, view.Rooms)
	}
	patch, err := a.roomFloorsPatch(json.RawMessage(`{"map_id":2,"revision":"` + view.Revision + `","rooms":[{"id":1,"floor":3},{"id":2,"floor":0}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(patch); string(b) != `{"mapid":2,"nsm":{"1":{"direction":90,"material":1},"2":{"material":0}}}` {
		t.Error(string(b))
	}
	for _, bad := range []string{`[{"id":1,"floor":5}]`, `[{"id":9,"floor":2}]`, `[{"id":1,"floor":2},{"id":1,"floor":2}]`, `[]`} {
		if _, err := a.roomFloorsPatch(json.RawMessage(`{"map_id":2,"revision":"` + view.Revision + `","rooms":` + bad + `}`)); err == nil {
			t.Error("accepted", bad)
		}
	}
	if floorKind(nil, nil) != 0 || floorKind(ptr(1), nil) != 1 || floorKind(ptr(1), ptr(90)) != 3 || floorKind(ptr(4), nil) != 4 || floorKind(ptr(7), nil) != 0 {
		t.Error("floorKind")
	}
}

func ptr(v int) *int { return &v }
