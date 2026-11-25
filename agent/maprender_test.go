package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
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
	// While mopping, the floor along the path is tinted blue.
	// The top-right room cell faces outside on two sides: its outer 2 px are the border, (6, 2) is floor.
	if got := color.RGBAModel.Convert(img.At(7, 1)).(color.RGBA); got != borderColour {
		t.Fatal("room border", got)
	}
	room := color.RGBAModel.Convert(img.At(6, 2)).(color.RGBA)
	frame := testLiveFrame(t, 'P', fmt.Sprintf(`{"robot":[5000,5000],"curid":"2","tr":"W-75,-125L50,0","mtid":"r","timestamp_ms":%d}`, time.Now().UnixMilli()))
	writeFile(t, a, iotLogFile, `"siid":6,"piid":1,"value":"`+frame+`"`+"\n")
	if b, err = a.mapPNG(); err != nil {
		t.Fatal(err)
	}
	img, _ = png.Decode(bytes.NewReader(b))
	if got := color.RGBAModel.Convert(img.At(6, 2)).(color.RGBA); got.B <= room.B || got.R >= room.R {
		t.Fatal("mopped floor not tinted", room, got)
	}
	// Coming back over the same floor after driving away is a second pass: deeper tint.
	once := color.RGBAModel.Convert(img.At(7, 1)).(color.RGBA)
	frame = testLiveFrame(t, 'P', fmt.Sprintf(`{"robot":[5000,5000],"curid":"2","tr":"W-75,-125L50,0L1000,0L-1000,0","mtid":"r","timestamp_ms":%d}`, time.Now().UnixMilli()))
	writeFile(t, a, iotLogFile, `"siid":6,"piid":1,"value":"`+frame+`"`+"\n")
	b, _ = a.mapPNG()
	img, _ = png.Decode(bytes.NewReader(b))
	if got := color.RGBAModel.Convert(img.At(7, 1)).(color.RGBA); got.B <= once.B {
		t.Fatal("second pass not deeper", once, got)
	}
	// Floor found this run that the saved map lacks: a run frame from (-100,-100) puts its first cell on
	// the saved map's empty top-left cell; its other new cells lie above the picture and are clipped.
	run, _ := decodeSavedFrame(syntheticFrame(t, 2))
	ox := int16(-100)
	binary.LittleEndian.PutUint16(run[23:25], uint16(ox))
	binary.LittleEndian.PutUint16(run[25:27], uint16(ox))
	meta := fmt.Sprintf(`{"robot":[5000,5000],"curid":"2","tr":"","mtid":"r","timestamp_ms":%d}`, time.Now().UnixMilli())
	writeFile(t, a, liveFrameFiles[0], strings.TrimRight(encodeTestFrame(t, append(run[:36], []byte(meta)...)), "="))
	writeFile(t, a, iotLogFile, "")
	if b, err = a.mapPNG(); err != nil {
		t.Fatal(err)
	}
	img, _ = png.Decode(bytes.NewReader(b))
	if got := color.RGBAModel.Convert(img.At(1, 1)).(color.RGBA); got != newFloorColour || img.Bounds().Dy() != 9 {
		t.Fatal("new floor cell", got, img.Bounds())
	}
}
