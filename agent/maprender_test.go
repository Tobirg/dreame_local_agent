package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// savedMap writes a 3x3 map of 50 mm cells from (-100,-200); cells: row0 = 1,1,255  row1 = 1,2,2  row2 = 0,2,2.
func savedMap(t *testing.T, a *agent, meta string) []byte {
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:36], []byte(meta)...)
	maps, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"map": encodeTestFrame(t, raw)}}})
	writeFile(t, a, "/data/log/map_info.bin", string(maps))
	return raw
}

func render(t *testing.T, a *agent, o mapOpts) string {
	b, err := a.mapSVG(o)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMapSVG(t *testing.T) {
	a := testAgent(t)
	if _, err := a.mapSVG(mapOpts{}); err == nil {
		t.Fatal("rendered without a saved map")
	}
	savedMap(t, a, `{"origin":[-100,-200]}`)
	svg := render(t, a, mapOpts{})
	for _, want := range []string{`<svg xmlns=`, `width="9" height="9"`, `id="r1"`, `id="r2"`, `<text x=`} {
		if !strings.Contains(svg, want) {
			t.Fatal("missing", want, svg)
		}
	}
	if strings.Contains(svg, `id="r0"`) || strings.Contains(svg, `id="r255"`) {
		t.Fatal("empty cells and walls are not rooms")
	}
	// The robot with no station is not drawn; with one it sits on it.
	if strings.Contains(svg, `fill="#f4f7f9"`) {
		t.Fatal("robot without a station or a run")
	}
	savedMap(t, a, `{"origin":[-100,-200],"charger":[-75,-175]}`)
	if svg = render(t, a, mapOpts{}); !strings.Contains(svg, `fill="#5edb72"`) || !strings.Contains(svg, `<circle cx="1.5" cy="7.5"`) {
		t.Fatal("station and the robot on it", svg)
	}
}

func TestMapSVGBaseAndOptions(t *testing.T) {
	a := testAgent(t)
	savedMap(t, a, `{"origin":[-100,-200],"charger":[-75,-175]}`)
	full := render(t, a, mapOpts{})
	base := render(t, a, mapOptsFromQuery(url.Values{"base": {"1"}, "selected": {"1"}, "floors": {"1:2,x:y"}, "layers": {"furniture,thresholds"}}))
	if strings.Contains(base, `#5edb72`) || strings.Contains(base, `font-weight="600"`) {
		t.Fatal("the base layer has no station and no room names")
	}
	if !strings.Contains(full, `font-weight="600"`) {
		t.Fatal("the picture for Home Assistant names the rooms")
	}
	if !strings.Contains(base, `fill="url(#f2)"`) {
		t.Fatal("room 1 asked for tiles")
	}
	if !strings.Contains(base, "#444e63") || !strings.Contains(base, "#7fd9c2") {
		t.Fatal("room 2 is dimmed, the selected room 1 is not", base)
	}
	o := mapOptsFromQuery(url.Values{"base": {"1"}, "selected": {"2"}, "floors": {"1:2,x:y"}, "layers": {"furniture,thresholds"}})
	if !o.base || !o.selected[2] || o.floors[1] != 2 || len(o.floors) != 1 || !o.layers["furniture"] {
		t.Fatal(o)
	}
}

func TestMapSVGOverlaysAndPad(t *testing.T) {
	a := testAgent(t)
	// Furniture half over the right edge of the 3x3 map, a no-go zone and a threshold on it.
	savedMap(t, a, `{"origin":[-100,-200]}`)
	v, err := a.mapView()
	if err != nil {
		t.Fatal(err)
	}
	v.NoGo = [][]int{{-90, -190, -60, -160}}
	v.Furniture = []furnitureItem{{Type: 2, X: -50, Y: -150, Width: 400, Height: 100, Rotation: 90}}
	v.Passable = [][]int{{-100, -150, -50, -150}}
	if v.overlayPad() < 10 {
		t.Fatal("furniture past the edge must grow the picture", v.overlayPad())
	}
	empty := mapView{Width: 10, Height: 10, GridSize: 50, Origin: []int{0, 0}, NoGo: [][]int{{100, 100, 400, 400}}}
	if empty.overlayPad() != 0 {
		t.Fatal("inside the map needs no room")
	}
}

func TestMapSVGLive(t *testing.T) {
	a := testAgent(t)
	savedMap(t, a, `{"origin":[-100,-200]}`)
	// While mopping, the path with the mop down is a band over the floor.
	frame := testLiveFrame(t, 'P', fmt.Sprintf(`{"robot":[5000,5000],"curid":"2","tr":"W-75,-125L50,0","mtid":"r","timestamp_ms":%d}`, time.Now().UnixMilli()))
	writeFile(t, a, iotLogFile, `"siid":6,"piid":1,"value":"`+frame+`"`+"\n")
	if svg := render(t, a, mapOpts{}); !regexp.MustCompile(`clip-path="url\(#floor\)"[^>]*stroke-width="18"><path d="M`).MatchString(svg) {
		t.Fatal("mopped band missing", svg)
	}
	// Floor found this run that the saved map lacks.
	run, _ := decodeSavedFrame(syntheticFrame(t, 2))
	ox := int16(-100)
	binary.LittleEndian.PutUint16(run[23:25], uint16(ox))
	binary.LittleEndian.PutUint16(run[25:27], uint16(ox))
	meta := fmt.Sprintf(`{"robot":[5000,5000],"curid":"2","tr":"","mtid":"r","timestamp_ms":%d}`, time.Now().UnixMilli())
	writeFile(t, a, liveFrameFiles[0], strings.TrimRight(encodeTestFrame(t, append(run[:36], []byte(meta)...)), "="))
	writeFile(t, a, iotLogFile, "")
	if svg := render(t, a, mapOpts{}); !strings.Contains(svg, `fill="#bfdcf5"`) {
		t.Fatal("new floor cell", svg)
	}
}

func TestRegionPath(t *testing.T) {
	block := func(x, y int) bool { return x >= 1 && x < 4 && y >= 1 && y < 3 } // 3x2 cells
	if d := regionPath(6, 6, block); !strings.HasPrefix(d, "M") || strings.Count(d, "M") != 1 || !strings.HasSuffix(d, "Z") {
		t.Fatal(d)
	}
	if d := regionPath(6, 6, func(x, y int) bool { return x == 2 && y == 2 }); d != "" {
		t.Fatal("a single cell is speckle", d)
	}
	ring := func(x, y int) bool {
		return x >= 0 && x < 6 && y >= 0 && y < 6 && !(x >= 2 && x < 4 && y >= 2 && y < 4)
	}
	if d := regionPath(6, 6, ring); strings.Count(d, "M") != 2 {
		t.Fatal("outer outline and the hole", d)
	}
}
