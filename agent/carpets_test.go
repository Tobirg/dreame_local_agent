package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestCarpetTypes(t *testing.T) {
	a := testAgent(t)
	view := restrictionsFixture(t, a, `{"addcpt":[]},"carpet_polygon":{"3":[[-100,-200,0,-200,0,-100],7],"4":[[-100,-200,0,-200,0,-100],6]}`)
	if len(view.Carpets) != 2 || view.Carpets[0].Type != 7 || view.Carpets[1].Type != 6 {
		t.Fatalf("%+v", view.Carpets)
	}
	edit := func(types map[string]int) (map[string]any, error) {
		raw, _ := json.Marshal(map[string]any{"map_id": view.MapID, "revision": view.Revision, "types": types})
		return a.carpetTypesPatch(raw)
	}
	got, err := edit(map[string]int{"3": 5})
	if b, _ := json.Marshal(got); err != nil || string(b) != `{"carpetmaterial":{"addcpt":[],"origincpt":[[3,5],[4,6]],"roomcpt":[]}}` {
		t.Fatal(string(b), err)
	}
	for _, bad := range []map[string]int{{"9": 5}, {"3": 4}, {}} {
		if _, err := edit(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}

func TestCarpetClean(t *testing.T) {
	a := testAgent(t)
	restrictionsFixture(t, a, `{"addcpt":[]},"carpet_polygon":{"2":[[-100,-200,0,-200,0,-100],7],"3":[[-100,-200,0,-200,0,-100],7]}`)
	if err := os.MkdirAll(filepath.Dir(a.path(roomSettingsFile)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.path(roomSettingsFile), []byte(`{"clean_sets":{},"carpet_clean_set":{"2":[[0,1,-1],[0,2,3]]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	view, err := a.mapView()
	if err != nil || view.Carpets[0].Clean == nil || *view.Carpets[0].Clean != 3 || view.Carpets[1].Clean != nil {
		t.Fatalf("%+v %v", view.Carpets, err)
	}
	edit := func(clean map[string]int) (map[string]any, error) {
		raw, _ := json.Marshal(map[string]any{"map_id": view.MapID, "revision": view.Revision, "clean": clean})
		return a.carpetCleanPatch(raw)
	}
	got, err := edit(map[string]int{"2": -1})
	if b, _ := json.Marshal(got); err != nil || string(b) != `{"carpetcleanset":[[0,2,-1]]}` {
		t.Fatal(string(b), err)
	}
	for _, bad := range []map[string]int{{"2": 4}, {"3": 1}, {"9": 1}} {
		if _, err := edit(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}
