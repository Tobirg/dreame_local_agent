package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFurniture(t *testing.T) {
	a := testAgent(t)
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	// 3x3 cells of 50 mm from (-100,-200); cells are 1 1 255 / 1 2 2 / 0 2 2 (row 0 at y -200).
	rows := `[[0,2,5,1750,2100,0,-4425.0,-5025.0,0.0,180.0,0.0,0.0,1.0,1,0],[0,28,5,650,650,0,175.0,-4425.0,0.0,0.0,0.0,0.0,1.0,1,0]]`
	raw = append(raw[:36], []byte(`{"origin":[-100,-200],"funiture_info":`+rows+`}`)...)
	body, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"map": encodeTestFrame(t, raw)}}})
	writeFile(t, a, "/data/log/map_info.bin", string(body))
	view, err := a.mapView()
	if err != nil || !view.FurnitureEditable || len(view.Furniture) != 2 || view.Furniture[0].Type != 2 || view.Furniture[0].Rotation != 180 || view.Furniture[1].X != 175 {
		t.Fatalf("%+v %v", view.Furniture, err)
	}
	edit := func(req string) (string, error) {
		got, err := a.furniturePatch(json.RawMessage(`{"map_id":2,"revision":"` + view.Revision + `",` + req + `}`))
		b, _ := json.Marshal(got)
		return string(b), err
	}
	// Removing one keeps the other row exactly as stored; a new one gets the room under its centre.
	got, err := edit(`"remove":[0],"add":[{"type":7,"x":-10,"y":-140,"width":500,"height":400,"rotation":90}]`)
	if err != nil || got != `{"mapfurniture_v2":[[0,28,5,650,650,0,175.0,-4425.0,0.0,0.0,0.0,0.0,1.0,1,0],[0,7,2,500,400,0,-10,-140,0,90,0,0,1,1,0]]}` {
		t.Fatal(got, err)
	}
	if _, err := edit(`"remove":[],"add":[{"type":7,"x":-90,"y":-80,"width":500,"height":400,"rotation":0}]`); err == nil || !strings.Contains(err.Error(), "inside a room") {
		t.Fatal("furniture outside rooms accepted", err)
	}
	if _, err := edit(`"remove":[5],"add":[]`); err == nil {
		t.Fatal("unknown index accepted")
	}
	if _, err := edit(`"remove":[],"add":[{"type":26,"x":-10,"y":-140,"width":500,"height":400,"rotation":0}]`); err == nil {
		t.Fatal("unknown type accepted")
	}
}
