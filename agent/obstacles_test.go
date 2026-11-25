package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestObstacles(t *testing.T) {
	a := testAgent(t)
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	maps, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"map": encodeTestFrame(t, append(raw[:36], []byte(`{"origin":[-100,-200]}`)...))}}})
	writeFile(t, a, "/data/log/map_info.bin", string(maps))
	if v, _ := a.mapView(); v.Obstacles == nil || len(v.Obstacles) != 0 || v.ObstaclesAt != nil {
		t.Fatal("no obstacles known yet", v.Obstacles)
	}

	// The run's final frame as the firmware keeps it for the cloud: numbers as strings.
	run := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	writeFile(t, a, fdsLogDir+"/1793114190", testLiveFrame(t, 'I', fmt.Sprintf(`{"curid":"2","timestamp_ms":%d,"ai_obstacle":[["-3520.000000","-530.000000","137","0.955322","0","0"],["1","2"],["-4110.000000","-610.000000","133","0.959198","1795437306","/data/record/ai_image/1.jpg","151085971","0.721741","0.387866","0.143204","0.184549","2","0","0"]]}`, run.UnixMilli())))
	// Later runs whose final frame has no "ai_obstacle" (seen on r2505 since 2026-11-23) are skipped.
	writeFile(t, a, fdsLogDir+"/1793114999", testLiveFrame(t, 'I', fmt.Sprintf(`{"curid":"2","timestamp_ms":%d}`, run.Add(time.Minute).UnixMilli())))
	v, _ := a.mapView()
	if !reflect.DeepEqual(v.Obstacles, [][]int{{-3520, -530, 137, 96}, {-4110, -610, 133, 96}}) || v.ObstaclesAt == nil || !v.ObstaclesAt.Equal(run) {
		t.Fatal(v.Obstacles, v.ObstaclesAt)
	}
	// Kept after the firmware's copy is gone.
	if err := os.RemoveAll(a.path(fdsLogDir)); err != nil {
		t.Fatal(err)
	}
	if v, _ := a.mapView(); len(v.Obstacles) != 2 {
		t.Fatal("kept list lost", v.Obstacles)
	}
	// Another map's list is not drawn on this one.
	if o := a.obstacles(3); len(o.List) != 0 || o.MapID != 3 {
		t.Fatal(o)
	}
	// A newer run that found nothing replaces the list; a frame without the key does not.
	frames := ""
	for i, meta := range []string{`"ai_obstacle":[]`, `"robot":[0,0]`} {
		frame := testLiveFrame(t, 'P', fmt.Sprintf(`{"curid":"2","timestamp_ms":%d,%s}`, time.Now().Add(time.Duration(i)*time.Second).UnixMilli(), meta))
		frames += `"siid":6,"piid":1,"value":"` + frame + `"` + "\n"
	}
	writeFile(t, a, iotLogFile, frames)
	if v, _ := a.mapView(); len(v.Obstacles) != 0 || v.ObstaclesAt == nil {
		t.Fatal("the newer empty list wins", v.Obstacles)
	}
}

func TestObstaclesOnMapPNG(t *testing.T) {
	a := testAgent(t)
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	maps, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"map": encodeTestFrame(t, append(raw[:36], []byte(`{"origin":[-100,-200]}`)...))}}})
	writeFile(t, a, "/data/log/map_info.bin", string(maps))
	// A sock (hazard) at the centre of the 3x3 map of 50 mm cells: image pixel (4, 4).
	writeFile(t, a, obstaclesFile, `{"map_id":2,"at":"2026-11-22T15:00:00Z","obstacles":[[-25,-125,134,90]]}`)
	b, err := a.mapPNG(mapOpts{})
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(b))
	if got := color.RGBAModel.Convert(img.At(4, 4)).(color.RGBA); got != hazardColour {
		t.Fatal("obstacle", got)
	}
}
