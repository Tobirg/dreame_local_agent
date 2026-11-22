package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func restrictionsFixture(t *testing.T, a *agent, vw string) mapView {
	t.Helper()
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	// 3x3 cells of 50 mm from (-100,-200): x -100..50, y -200..-50.
	raw = append(raw[:36], []byte(`{"origin":[-100,-200],"vw":`+vw+`}`)...)
	view, err := makeMapView(raw, "dreame.vacuum.r2505")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"map": encodeTestFrame(t, raw)}}})
	if err := os.WriteFile(a.path("/data/log/map_info.bin"), body, 0600); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestRestrictionsReadAndPatch(t *testing.T) {
	a := testAgent(t)
	view := restrictionsFixture(t, a, `{"line":[[-90,-190,-60,-190]],"cliff":[[1,2,3,4]],"rect":[],"mop":[],"nocpt":[],"addcpt":[]}`)
	if !view.RestrictionsEditable || len(view.Walls) != 1 || len(view.NoGo) != 0 {
		t.Fatalf("%+v", view)
	}
	edit := func(walls, nogo, nomop [][]int) (map[string]any, error) {
		b, _ := json.Marshal(map[string]any{"map_id": 2, "revision": view.Revision, "walls": walls, "no_go_zones": nogo, "no_mop_zones": nomop})
		return a.restrictionsPatch(b)
	}
	patch, err := edit([][]int{}, [][]int{{-90, -190, 0, -100}}, [][]int{{-50, -150, 40, -60}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(patch); string(got) != `{"mapid":2,"vw":{"line":[],"mop":[[-50,-150,40,-60]],"rect":[[-90,-190,0,-100]]}}` {
		t.Fatal(string(got))
	}
	for _, bad := range [][3][][]int{
		{{{-90, -190, -90, -190}}, {}, {}}, // wall of one point
		{{}, {{-90, -190, -90, -100}}, {}}, // zone without width
		{{}, {{-90, -190, 500, -100}}, {}}, // outside the map
		{nil, {}, {}},                      // missing list
	} {
		if _, err := edit(bad[0], bad[1], bad[2]); err == nil {
			t.Error("accepted", bad)
		}
	}
	stale, _ := json.Marshal(map[string]any{"map_id": 2, "revision": "old", "walls": [][]int{}, "no_go_zones": [][]int{}, "no_mop_zones": [][]int{}})
	if _, err := a.restrictionsPatch(stale); err == nil || !strings.Contains(err.Error(), "map changed") {
		t.Fatal("stale revision accepted", err)
	}
	// Through the RPC it needs control, like every map edit.
	a.cfg.AllowControl = false
	if _, err := a.call(context.Background(), request{Method: "local.restrictions", Params: stale}); err == nil {
		t.Fatal("edit without allow_control")
	}
}

func TestRestrictionsUnknownShapeIsReadOnly(t *testing.T) {
	a := testAgent(t)
	if view := restrictionsFixture(t, a, `{"line":[[1,2,3,4,5]],"rect":[],"mop":[]}`); view.RestrictionsEditable {
		t.Fatal("unknown row shape must not be editable")
	}
	if view := restrictionsFixture(t, a, `null`); view.RestrictionsEditable {
		t.Fatal("map without vw must not be editable")
	}
}
