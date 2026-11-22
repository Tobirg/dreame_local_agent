package main

import (
	"bytes"
	"encoding/json"
	"image/color"
	"image/png"
	"testing"
)

func TestMapPNG(t *testing.T) {
	a := testAgent(t)
	if _, err := a.mapPNG(); err == nil {
		t.Fatal("rendered without a saved map")
	}
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	// 3x3 cells of 50 mm from (-100,-200); cells: row0 = 1,1,255  row1 = 1,2,2  row2 = 0,2,2.
	raw = append(raw[:36], []byte(`{"origin":[-100,-200],"charger":[-75,-175]}`)...)
	maps, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"map": encodeTestFrame(t, raw)}}})
	writeFile(t, a, "/data/log/map_info.bin", string(maps))
	b, err := a.mapPNG()
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 9 || img.Bounds().Dy() != 9 {
		t.Fatal(img.Bounds())
	}
	// Map row 0 (bottom) cell 2 is a wall; map row 2 (top) cell 0 is empty.
	if got := color.RGBAModel.Convert(img.At(7, 8)).(color.RGBA); got != wallColour {
		t.Fatal("bottom-right cell", got)
	}
	if _, _, _, alpha := img.At(1, 1).RGBA(); alpha != 0 {
		t.Fatal("top-left cell must stay empty")
	}
	// The station at (-75,-175) is cell (0,0): bottom-left.
	if got := color.RGBAModel.Convert(img.At(1, 7)).(color.RGBA); got != chargerColour {
		t.Fatal("station", got)
	}
}
