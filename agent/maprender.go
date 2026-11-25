package main

import (
	"errors"
	"fmt"
	"image/color"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// The map picture is one SVG: the web panel shows it under its own canvas and the MQTT module publishes it
// to Home Assistant, so both enlarge it without loss. Units are the old picture's pixels (3 per map cell),
// which keeps the calibration points of the Home Assistant card unchanged.
var (
	roomPalette = []color.RGBA{{0x7f, 0xd9, 0xc2, 255}, {0xa3, 0xbc, 0xec, 255}, {0xe5, 0xbd, 0x8a, 255}, {0xbc, 0xac, 0xeb, 255},
		{0xa7, 0xd3, 0x9a, 255}, {0xe3, 0xa5, 0xb4, 255}, {0x93, 0xcd, 0xd9, 255}, {0xd4, 0xd0, 0x93, 255}} // same colours as ui/app.js
	wallColour = color.RGBA{0xa6, 0xba, 0xd0, 255} // border cells, the room outline of the Dreame app
)

// Obstacle types drawn as hazards: socket, wire, sock, faeces, cable, liquid stain, hair (names as in the Dreame app).
var hazardTypes = map[int]bool{130: true, 131: true, 134: true, 135: true, 138: true, 139: true, 225: true}

const mapScale = 3 // picture units per map cell

// Mop pad span of the X50 (two round pads); the band crossing into the next room is real, the pads reach over thresholds.
const mopWidth = 300 // robot millimetres

// mapOpts: base is what the web panel takes as its map layer (floors, walls, room numbers, and the saved
// groups named in layers); the panel draws the rest itself, editable. selected dims the other rooms, floors
// replaces a room's floor kind (the panel's unsaved choice).
type mapOpts struct {
	base     bool
	selected map[int]bool
	floors   map[int]int
	layers   map[string]bool
}

// The panel asks for the base layer on every page load; the last one is kept (the robot's CPU is slow).
var baseCache struct {
	sync.Mutex
	key string
	svg []byte
}

// mapOptsFromQuery reads ?base=1&selected=1,3&floors=1:2,3:0&layers=furniture,thresholds.
func mapOptsFromQuery(q url.Values) mapOpts {
	o := mapOpts{base: q.Get("base") == "1", selected: map[int]bool{}, floors: map[int]int{}, layers: map[string]bool{}}
	for _, v := range strings.Split(q.Get("layers"), ",") {
		o.layers[v] = true
	}
	for _, v := range strings.Split(q.Get("selected"), ",") {
		if id, err := strconv.Atoi(v); err == nil {
			o.selected[id] = true
		}
	}
	for _, v := range strings.Split(q.Get("floors"), ",") {
		if id, kind, ok := strings.Cut(v, ":"); ok {
			i, e1 := strconv.Atoi(id)
			k, e2 := strconv.Atoi(kind)
			if e1 == nil && e2 == nil {
				o.floors[i] = k
			}
		}
	}
	return o
}

func colourHex(c color.RGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

func scale(c color.RGBA, percent int) color.RGBA {
	return color.RGBA{uint8(int(c.R) * percent / 100), uint8(int(c.G) * percent / 100), uint8(int(c.B) * percent / 100), 255}
}

func (v mapView) rawPx(p []int) (int, int) {
	return (p[0] - v.Origin[0]) * mapScale / v.GridSize, (v.Height*v.GridSize - (p[1] - v.Origin[1])) * mapScale / v.GridSize
}

// overlayPad is how far the saved overlays (zones, walls, thresholds, carpets, furniture, obstacles,
// the station) reach past the map, in picture pixels; the picture grows by that on every side.
// shortcut: capped at 150 pixels, a stray far-away point is cut instead of growing the picture.
func (v mapView) overlayPad() int {
	w, h := v.Width*mapScale, v.Height*mapScale
	over := 0
	note := func(x, y, r int) { over = max(over, r-x, x+r-w, r-y, y+r-h) }
	pt := func(p []int) {
		if len(p) >= 2 {
			x, y := v.rawPx(p)
			note(x, y, 0)
		}
	}
	for _, list := range [][][]int{v.NoGo, v.NoMop, v.CarpetsAdded, v.CarpetsIgnored, v.Ramps, v.Walls, v.Passable, v.Impassable} {
		for _, r := range list {
			pt(r)
			if len(r) >= 4 {
				pt(r[2:])
			}
		}
	}
	for _, c := range v.Carpets {
		for i := 0; i+1 < len(c.Points); i += 2 {
			pt(c.Points[i : i+2])
		}
	}
	for _, f := range v.Furniture {
		x, y := v.rawPx([]int{f.X, f.Y})
		note(x, y, int(math.Hypot(float64(f.Width*mapScale), float64(f.Height*mapScale))/float64(v.GridSize)/2)+2)
	}
	for _, o := range v.Obstacles {
		pt(o)
	}
	pt(v.Charger)
	if over <= 0 {
		return 0
	}
	return min(over+8, 150)
}

// mapCard is what a Home Assistant map card needs next to map.png: how robot millimetres land
// on the image (three points, the xiaomi-vacuum-map-card format) and the room names.
func (a *agent) mapCard() (map[string]any, error) {
	view, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if view.Empty || len(view.Origin) != 2 {
		return nil, errors.New("no saved map")
	}
	pad := view.overlayPad() // the picture has room around the map for overlays that reach past it
	points := []map[string]any{}
	for _, p := range [][2]int{{0, 0}, {1000, 0}, {0, 1000}} {
		x := float64(p[0]-view.Origin[0])*mapScale/float64(view.GridSize) + float64(pad)
		y := float64(view.Height*view.GridSize-(p[1]-view.Origin[1]))*mapScale/float64(view.GridSize) + float64(pad)
		points = append(points, map[string]any{"vacuum": map[string]int{"x": p[0], "y": p[1]}, "map": map[string]float64{"x": x, "y": y}})
	}
	// id -> name, centre and outline in robot millimetres: the card's "Generate rooms config" reads these.
	rooms := map[string]any{}
	for _, r := range view.Rooms {
		outline := [][2]int{}
		for _, p := range roomOutline(view, r.ID) {
			outline = append(outline, [2]int{view.Origin[0] + p[0]*view.GridSize, view.Origin[1] + p[1]*view.GridSize})
		}
		rooms[strconv.Itoa(r.ID)] = map[string]any{"name": r.Name, "outline": outline,
			"x": view.Origin[0] + int(r.X*float64(view.GridSize)), "y": view.Origin[1] + int(r.Y*float64(view.GridSize))}
	}
	return map[string]any{"map_id": view.MapID, "calibration_points": points, "rooms": rooms}, nil
}

// roomOutline is the longest boundary loop of the room's cells, as cell-corner coordinates
// with straight runs merged. shortcut: holes and separate pieces of a room are left out.
func roomOutline(view mapView, id int) [][2]int {
	in := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < view.Width && y < view.Height && int(view.Pixels[y*view.Width+x]) == id
	}
	next := map[[2]int][][2]int{} // boundary edges, counter-clockwise around the room
	edge := func(a, b [2]int) { next[a] = append(next[a], b) }
	for y := 0; y < view.Height; y++ {
		for x := 0; x < view.Width; x++ {
			if !in(x, y) {
				continue
			}
			if !in(x, y-1) {
				edge([2]int{x, y}, [2]int{x + 1, y})
			}
			if !in(x+1, y) {
				edge([2]int{x + 1, y}, [2]int{x + 1, y + 1})
			}
			if !in(x, y+1) {
				edge([2]int{x + 1, y + 1}, [2]int{x, y + 1})
			}
			if !in(x-1, y) {
				edge([2]int{x, y + 1}, [2]int{x, y})
			}
		}
	}
	var best [][2]int
	for y := 0; y <= view.Height; y++ {
		for x := 0; x <= view.Width; x++ {
			start := [2]int{x, y}
			loop := [][2]int{}
			for p := start; len(next[p]) > 0; {
				q := next[p][len(next[p])-1]
				next[p] = next[p][:len(next[p])-1]
				loop = append(loop, p)
				p = q
			}
			if len(loop) > len(best) {
				best = loop
			}
		}
	}
	out := [][2]int{}
	for i, p := range best {
		a, b := best[(i+len(best)-1)%len(best)], best[(i+1)%len(best)]
		if (a[0] == p[0] && p[0] == b[0]) || (a[1] == p[1] && p[1] == b[1]) {
			continue // middle of a straight run
		}
		out = append(out, p)
	}
	return out
}
