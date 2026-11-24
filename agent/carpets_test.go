package main

import (
	"encoding/json"
	"testing"
)

func TestCarpets(t *testing.T) {
	var v mapView
	v.carpets(true, map[string][]json.RawMessage{"3": {json.RawMessage(`[-1850.0,2600.0,-1800.0,2650.0,-1500.0,2650.0]`), json.RawMessage(`7`)}, "x": nil},
		[][]int{{-4700, 850, -4300, 1250}}, nil)
	if !v.CarpetsEditable || len(v.Carpets) != 1 || v.Carpets[0].ID != 3 || v.Carpets[0].Points[4] != -1500 || len(v.CarpetsAdded) != 1 || v.CarpetsIgnored == nil {
		t.Fatalf("%+v", v)
	}
	v.carpets(true, nil, [][]int{{1, 2, 3, 4, 1}}, nil)
	if v.CarpetsEditable {
		t.Fatal("unknown row shape left editable")
	}

	a := testAgent(t)
	// 3x3 cells of 50 mm from (-100,-200): x -100..50, y -200..-50.
	view := restrictionsFixture(t, a, `{"line":[],"rect":[],"mop":[],"nocpt":[],"addcpt":[]}`)
	edit := func(added, ignored [][]int) (map[string]any, error) {
		raw, _ := json.Marshal(map[string]any{"map_id": view.MapID, "revision": view.Revision, "carpets_added": added, "carpets_ignored": ignored})
		return a.carpetsPatch(raw)
	}
	got, err := edit([][]int{{40, -60, -90, -190}}, [][]int{{-100, -200, -10, -110}})
	if b, _ := json.Marshal(got); err != nil || string(b) != `{"cpt":{"addcpt":[[-100,-200,50,-50]],"nocpt":[[-100,-200,0,-100]]}}` {
		t.Fatal(string(b), err)
	}
	if _, err := edit([][]int{{-100, -200, -60, -160}}, [][]int{}); err == nil {
		t.Fatal("tiny carpet accepted")
	}
	if _, err := edit([][]int{{-100, -200, 500, -50}}, [][]int{}); err == nil {
		t.Fatal("carpet outside the map accepted")
	}
	if _, err := edit(nil, [][]int{}); err == nil {
		t.Fatal("missing list accepted")
	}
}
