package main

import (
	"errors"
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
)

// num prints a picture coordinate with at most one decimal.
func num(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0")
}

// regionPath is the outline of the cells for which in(x, y) holds (image rows, y down) as an SVG path in
// picture units: every boundary loop, straight runs merged, sharp corners. Loops under 2 cells of area (speckle) are left out; fill it with
// fill-rule evenodd so holes stay open.
func regionPath(w, h int, in func(x, y int) bool) string {
	next := map[[2]int][][2]int{}
	edge := func(a, b [2]int) { next[a] = append(next[a], b) }
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !in(x, y) {
				continue
			}
			if y == 0 || !in(x, y-1) {
				edge([2]int{x, y}, [2]int{x + 1, y})
			}
			if x == w-1 || !in(x+1, y) {
				edge([2]int{x + 1, y}, [2]int{x + 1, y + 1})
			}
			if y == h-1 || !in(x, y+1) {
				edge([2]int{x + 1, y + 1}, [2]int{x, y + 1})
			}
			if x == 0 || !in(x-1, y) {
				edge([2]int{x, y + 1}, [2]int{x, y})
			}
		}
	}
	var b strings.Builder
	for y := 0; y <= h; y++ {
		for x := 0; x <= w; x++ {
			for len(next[[2]int{x, y}]) > 0 {
				loop := [][2]int{}
				for p := [2]int{x, y}; len(next[p]) > 0; {
					q := next[p][len(next[p])-1]
					next[p] = next[p][:len(next[p])-1]
					loop = append(loop, p)
					p = q
				}
				smoothLoop(&b, loop)
			}
		}
	}
	return b.String()
}

func smoothLoop(b *strings.Builder, loop [][2]int) {
	// Merge straight runs.
	vs := [][2]int{}
	for i, p := range loop {
		a, c := loop[(i+len(loop)-1)%len(loop)], loop[(i+1)%len(loop)]
		if (a[0] == p[0] && p[0] == c[0]) || (a[1] == p[1] && p[1] == c[1]) {
			continue
		}
		vs = append(vs, p)
	}
	area := 0
	for i, p := range vs {
		q := vs[(i+1)%len(vs)]
		area += p[0]*q[1] - q[0]*p[1]
	}
	if len(vs) < 4 || abs(area)/2 < 2 {
		return
	}
	for i, v := range vs {
		c := "L"
		if i == 0 {
			c = "M"
		}
		fmt.Fprintf(b, "%s%s %s", c, num(float64(v[0])*mapScale), num(float64(v[1])*mapScale))
	}
	b.WriteString("Z")
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// mapSVG renders the saved map with the station and, while cleaning, the robot and its path.
func (a *agent) mapSVG(o mapOpts) ([]byte, error) {
	view, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if view.Empty || len(view.Origin) != 2 {
		return nil, errors.New("no saved map")
	}
	key := fmt.Sprintf("%s|%v|%v|%v", view.Revision, o.selected, o.floors, o.layers)
	if o.base {
		baseCache.Lock()
		defer baseCache.Unlock()
		if baseCache.key == key {
			return baseCache.svg, nil
		}
	}
	pad := 0 // room around the map for overlays that reach past it; the panel's base layer has none
	if !o.base {
		pad = view.overlayPad()
	}
	want := func(group string) bool { return !o.base || o.layers[group] }
	W, H := view.Width*mapScale+2*pad, view.Height*mapScale+2*pad
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, W, H, W, H)
	// Floor patterns, laid out like the app's floors: tiles, planks along x, planks along y.
	b.WriteString(`<defs><g stroke="#000" stroke-opacity=".16" stroke-width="1" fill="none">` +
		`<pattern id="f2" width="36" height="36" patternUnits="userSpaceOnUse"><path d="M0 .5H36M.5 0V36"/></pattern>` +
		`<pattern id="f4" width="48" height="24" patternUnits="userSpaceOnUse"><path d="M0 .5H48M0 12.5H48M.5 0V12M24.5 12V24"/></pattern>` +
		`<pattern id="f3" width="24" height="48" patternUnits="userSpaceOnUse"><path d="M.5 0V48M12.5 0V48M0 .5H12M12 24.5H24"/></pattern></g>` +
		`<pattern id="c1" width="4" height="4" patternUnits="userSpaceOnUse"><path d="M2 0h2v2h-2zM0 2h2v2h-2z" fill="#000" fill-opacity=".15"/></pattern>` +
		`<pattern id="c2" width="4" height="4" patternUnits="userSpaceOnUse"><path d="M2 0h2v2h-2zM0 2h2v2h-2z" fill="#000" fill-opacity=".3"/></pattern>`)
	box := map[uint8][4]int{}
	for i, id := range view.Pixels {
		if id == 0 || id == 255 {
			continue
		}
		x, y := i%view.Width, i/view.Width
		bb, ok := box[id]
		if !ok {
			bb = [4]int{x, x, y, y}
		}
		box[id] = [4]int{min(bb[0], x), max(bb[1], x), min(bb[2], y), max(bb[3], y)}
	}
	kind := map[uint8]int{}
	for _, r := range view.Rooms {
		f, bb := r.Floor, box[uint8(r.ID)]
		if v, ok := o.floors[r.ID]; ok {
			f = v
		}
		if f == 1 { // planks along the longer side
			f = 3
			if bb[1]-bb[0] > bb[3]-bb[2] {
				f = 4
			}
		}
		kind[uint8(r.ID)] = f
	}
	ids := []int{}
	for id := range box {
		ids = append(ids, int(id))
	}
	sortInts(ids)
	at := func(id int) func(x, y int) bool { // image rows run down, map rows up
		return func(x, y int) bool { return int(view.Pixels[(view.Height-1-y)*view.Width+x]) == id }
	}
	for _, id := range ids {
		fmt.Fprintf(&b, `<path id="r%d" d="%s"/>`, id, regionPath(view.Width, view.Height, at(id)))
	}
	b.WriteString(`<clipPath id="floor">`)
	for _, id := range ids {
		fmt.Fprintf(&b, `<use href="#r%d" clip-rule="evenodd"/>`, id)
	}
	b.WriteString(`</clipPath></defs>`)
	fmt.Fprintf(&b, `<g transform="translate(%d %d)">`, pad, pad)
	// The saved outline first, in the wall colour; rooms go over it and grow by a third of a pixel so that
	// the seam between two rooms does not show the wall colour.
	fmt.Fprintf(&b, `<path fill="%s" fill-rule="evenodd" d="%s"/>`, colourHex(wallColour), regionPath(view.Width, view.Height, func(x, y int) bool {
		return view.Pixels[(view.Height-1-y)*view.Width+x] != 0
	}))
	dim := func(id int) bool { return len(o.selected) > 0 && !o.selected[id] }
	for _, id := range ids {
		c := roomPalette[(id-1)%len(roomPalette)]
		if dim(id) {
			c = scale(c, 42)
		}
		fmt.Fprintf(&b, `<use href="#r%d" fill="%s" stroke="%s" stroke-width=".7" fill-rule="evenodd"/>`, id, colourHex(c), colourHex(c))
		if k := kind[uint8(id)]; k >= 2 && k <= 4 {
			fmt.Fprintf(&b, `<use href="#r%d" fill="url(#f%d)" fill-rule="evenodd"/>`, id, k)
		}
	}
	for _, r := range view.Rooms { // room number badge and name, as the app; the panel draws its own order badges and names
		if o.base {
			break
		}
		x, y := (r.X+.5)*mapScale, (float64(view.Height)-r.Y-.5)*mapScale
		ring := roomPalette[(r.ID-1)%len(roomPalette)]
		op := 1.0
		if dim(r.ID) {
			op = .55
		}
		fmt.Fprintf(&b, `<g opacity="%s" font-family="system-ui,sans-serif" text-anchor="middle" dominant-baseline="central" fill="#fff">`, num(op))
		fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="7" fill="#17232d" stroke="%s" stroke-width="2"/>`, num(x), num(y-9), colourHex(ring))
		fmt.Fprintf(&b, `<text x="%s" y="%s" font-size="10" font-weight="700">%d</text>`, num(x), num(y-8.5), r.Num)
		fmt.Fprintf(&b, `<text x="%s" y="%s" font-size="11" font-weight="600" stroke="#17232d" stroke-opacity=".8" stroke-width="3" paint-order="stroke" stroke-linejoin="round">%s</text>`, num(x), num(y+8), html.EscapeString(r.Name))
		b.WriteString(`</g>`)
	}
	px := func(p []int) (float64, float64) {
		return float64(p[0]-view.Origin[0]) * mapScale / float64(view.GridSize), float64(view.Height*view.GridSize-(p[1]-view.Origin[1])) * mapScale / float64(view.GridSize)
	}
	pt := func(p []int) string { x, y := px(p); return num(x) + " " + num(y) }
	// Saved overlays, same colours as ui/app.js.
	if want("restrictions") {
		for _, c := range view.Carpets { // found by the robot: the app's texture
			var d strings.Builder
			for i := 0; i+1 < len(c.Points); i += 2 {
				d.WriteString(map[bool]string{true: "L", false: "M"}[i > 0] + pt(c.Points[i:i+2]))
			}
			fmt.Fprintf(&b, `<path d="%sZ" fill="url(#c1)"/>`, d.String())
		}
		for _, k := range []struct {
			list         [][]int
			stroke, fill string
			dash         bool
		}{
			{view.NoGo, "#ff4d4d", `fill="#ff4d4d" fill-opacity=".25"`, false},
			{view.NoMop, "#5aa9ff", `fill="#5aa9ff" fill-opacity=".25"`, false},
			{view.CarpetsAdded, "#000", `fill="url(#c2)"`, false},
			{view.CarpetsIgnored, "#8a8a8a", `fill="#8a8a8a" fill-opacity=".15"`, true},
			{view.Ramps, "#2f9e44", `fill="#2f9e44" fill-opacity=".25"`, false},
		} {
			for _, r := range k.list {
				if len(r) != 4 {
					continue
				}
				x0, y0 := px(r[:2])
				x1, y1 := px(r[2:4])
				dash := ""
				if k.dash {
					dash = ` stroke-dasharray="6 4"`
				}
				fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" %s stroke="%s" stroke-width="2"%s/>`,
					num(math.Min(x0, x1)), num(math.Min(y0, y1)), num(math.Abs(x1-x0)), num(math.Abs(y1-y0)), k.fill, k.stroke, dash)
			}
		}
		for _, w := range view.Walls {
			if len(w) == 4 {
				fmt.Fprintf(&b, `<path d="M%sL%s" stroke="#ff4d4d" stroke-width="2"/>`, pt(w[:2]), pt(w[2:4]))
			}
		}
	}
	if want("thresholds") {
		for _, k := range []struct {
			list   [][]int
			colour string
		}{{view.Passable, "#55f2aa"}, {view.Impassable, "#ff6b80"}} {
			for _, t := range k.list {
				if len(t) == 4 {
					fmt.Fprintf(&b, `<path d="M%sL%s" stroke="%s" stroke-width="2"/>`, pt(t[:2]), pt(t[2:4]), k.colour)
				}
			}
		}
	}
	if want("furniture") {
		for _, f := range view.Furniture {
			x, y := px([]int{f.X, f.Y})
			w, h := float64(f.Width*mapScale)/float64(view.GridSize), float64(f.Height*mapScale)/float64(view.GridSize)
			colour := "#c58b2b"
			if f.Type < 1 || f.Type > 25 {
				colour = "#888" // a type the app does not name
			}
			fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" transform="translate(%s %s) rotate(%s)" fill="%s" fill-opacity=".25" stroke="%s" stroke-width="2"/>`,
				num(-w/2), num(-h/2), num(w), num(h), num(x), num(y), num(-f.Rotation), colour, colour)
		}
	}
	if o.base {
		b.WriteString(`</g></svg>`)
		out := []byte(b.String())
		baseCache.svg, baseCache.key = out, key
		return out, nil
	}
	live := a.livePose()
	if !live.Live || live.MapID != view.MapID {
		live = livePose{}
	}
	// Floor and walls the robot found this run that the saved map lacks.
	for _, c := range live.NewCells {
		colour := `#bfdcf5`
		if c[2] == 2 {
			colour = `#8fa3b3`
		}
		x, y := px([]int{c[0], c[1] + live.GridSize})
		fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"/>`, num(x), num(y), num(float64(live.GridSize*mapScale)/float64(view.GridSize)), num(float64(live.GridSize*mapScale)/float64(view.GridSize)), colour)
	}
	// Mopped floor: each run of the path with the mop down is a band of the pad span; a band crossing an
	// earlier one deepens the tint, as a second pass does. Only the floor gets wet.
	b.WriteString(`<g clip-path="url(#floor)" fill="none" stroke="#4da3ff" stroke-opacity=".27" stroke-linecap="round" stroke-linejoin="round" stroke-width="` + num(float64(mopWidth*mapScale)/float64(view.GridSize)) + `">`)
	for i := 1; i < len(live.Path); {
		if len(live.Path[i]) < 3 || live.Path[i][2] != 1 {
			i++
			continue
		}
		d := "M" + pt(live.Path[i-1])
		for ; i < len(live.Path) && len(live.Path[i]) >= 3 && live.Path[i][2] == 1; i++ {
			d += "L" + pt(live.Path[i])
		}
		fmt.Fprintf(&b, `<path d="%s"/>`, d)
	}
	b.WriteString(`</g>`)
	if len(live.Path) > 1 {
		d := ""
		for i, p := range live.Path {
			d += map[bool]string{true: "L", false: "M"}[i > 0] + pt(p)
		}
		fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="#fff" stroke-opacity=".7" stroke-width="1.5"/>`, d)
	}
	for _, z := range live.Zones {
		x0, y0 := px(z[:2])
		x1, y1 := px(z[2:4])
		fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="none" stroke="#ffae67" stroke-width="2" stroke-dasharray="6 4"/>`,
			num(math.Min(x0, x1)), num(math.Min(y0, y1)), num(math.Abs(x1-x0)), num(math.Abs(y1-y0)))
	}
	for _, s := range live.Spots {
		x, y := px(s)
		fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="5" fill="#ffae67" stroke="#17232d" stroke-width="2"/>`, num(x), num(y))
	}
	for _, o := range view.Obstacles {
		x, y := px(o[:2])
		colour := "#ffd25e"
		if hazardTypes[o[2]] {
			colour = "#ff6b6b"
		}
		fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="6" fill="#17232d"/><circle cx="%s" cy="%s" r="4" fill="%s"/>`, num(x), num(y), num(x), num(y), colour)
	}
	// Sizes as in the app: the robot is 320 mm wide, the station 1.2 times the robot.
	r := math.Max(6, float64(160*mapScale)/float64(view.GridSize))
	charger := view.Charger
	if live.Charger != nil {
		charger = live.Charger
	}
	if charger != nil {
		x, y := px(charger)
		k := r * 1.2
		fmt.Fprintf(&b, `<g transform="translate(%s %s) scale(%s)"><circle r="10" fill="#5edb72"/><circle r="7.6" fill="#9ef0a8"/><path d="M1.5-6L-3.5.5H-.2L-1.5 6L3.5-.5H.2Z" fill="#5edb72"/></g>`, num(x), num(y), num(k/10))
	}
	robot := live.Robot
	if robot == nil {
		robot = charger // off a run the robot sits on the station and covers it
	}
	if robot != nil {
		x, y := px(robot)
		rim := math.Max(1.5, r*.14)
		fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="%s" fill="#f4f7f9" stroke="#6b7780" stroke-width="%s"/><circle cx="%s" cy="%s" r="%s" fill="#4da3ff"/>`,
			num(x), num(y), num(r-rim/2), num(rim), num(x), num(y), num(r*.4))
		if live.Robot != nil && live.Angle != nil {
			t := -float64(*live.Angle) * math.Pi / 180
			fmt.Fprintf(&b, `<path d="M%s %sL%s %s" stroke="#17232d" stroke-width="2"/>`, num(x+math.Cos(t)*r*.55), num(y+math.Sin(t)*r*.55), num(x+math.Cos(t)*r*.92), num(y+math.Sin(t)*r*.92))
		}
	}
	b.WriteString(`</g></svg>`)
	return []byte(b.String()), nil
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
