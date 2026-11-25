package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// Same colours as ui/app.js.
var (
	roomPalette = []color.RGBA{{0x7f, 0xd9, 0xc2, 255}, {0xa3, 0xbc, 0xec, 255}, {0xe5, 0xbd, 0x8a, 255}, {0xbc, 0xac, 0xeb, 255},
		{0xa7, 0xd3, 0x9a, 255}, {0xe3, 0xa5, 0xb4, 255}, {0x93, 0xcd, 0xd9, 255}, {0xd4, 0xd0, 0x93, 255}}
	wallColour     = color.RGBA{0xa6, 0xba, 0xd0, 255} // border cells, the room outline of the Dreame app
	pathColour     = color.RGBA{0xff, 0xff, 0xff, 200}
	robotColour    = color.RGBA{0x4d, 0xa3, 0xff, 255}
	chargerColour  = color.RGBA{0x55, 0xf2, 0xaa, 255}
	targetColour   = color.RGBA{0xff, 0xae, 0x67, 255} // zone and spot being cleaned
	newFloorColour = color.RGBA{0xbf, 0xdc, 0xf5, 255} // found this run, not in the saved map
	newWallColour  = color.RGBA{0x8f, 0xa3, 0xb3, 255}
	obstacleRing   = color.RGBA{0x17, 0x23, 0x2d, 255}
	obstacleColour = color.RGBA{0xff, 0xd2, 0x5e, 255}
	hazardColour   = color.RGBA{0xff, 0x6b, 0x6b, 255} // the robot can get tangled in or smear it
)

// Obstacle types drawn as hazards: socket, wire, sock, faeces, cable, liquid stain, hair (names as in the Dreame app).
var hazardTypes = map[int]bool{130: true, 131: true, 134: true, 135: true, 138: true, 139: true, 225: true}

const mapScale = 3 // output pixels per map cell, as the web UI draws it

// Mop pad span of the X50 (two round pads); the band crossing into the next room is real, the pads reach over thresholds.
const mopWidth = 300 // robot millimetres

var (
	mopColour = color.RGBA{0x4d, 0xa3, 0xff, 255} // blue, like a wet floor
	mopAlpha  = []int{70, 120, 165}               // 1, 2 and 3+ passes
)

// mapOpts: base draws only what the web panel takes as its map layer (floors, walls, room badges);
// the panel draws the rest itself, editable. selected dims the other rooms, floors replaces a room's floor
// kind (the panel's unsaved choice).
type mapOpts struct {
	base     bool
	selected map[int]bool
	floors   map[int]int
}

// mapOptsFromQuery reads ?base=1&selected=1,3&floors=1:2,3:0.
func mapOptsFromQuery(q url.Values) mapOpts {
	o := mapOpts{base: q.Get("base") == "1", selected: map[int]bool{}, floors: map[int]int{}}
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

// mapPNG renders the saved map with the station and, while cleaning, the robot and its path.
func (a *agent) mapPNG(o mapOpts) ([]byte, error) {
	view, err := a.mapView()
	if err != nil {
		return nil, err
	}
	if view.Empty || len(view.Origin) != 2 {
		return nil, errors.New("no saved map")
	}
	img := image.NewRGBA(image.Rect(0, 0, view.Width*mapScale, view.Height*mapScale))
	// Floor texture as in the web UI: 2 tiles, 3 planks along y, 4 along x; 1 picks by the room's longer side.
	box := map[uint8][4]int{}
	for i, id := range view.Pixels {
		if id == 0 || id == 255 {
			continue
		}
		x, y := i%view.Width, i/view.Width
		b, ok := box[id]
		if !ok {
			b = [4]int{x, x, y, y}
		}
		box[id] = [4]int{min(b[0], x), max(b[1], x), min(b[2], y), max(b[3], y)}
	}
	kind := map[uint8]int{}
	for _, r := range view.Rooms {
		f, b := r.Floor, box[uint8(r.ID)]
		if v, ok := o.floors[r.ID]; ok {
			f = v
		}
		if f == 1 {
			f = 3
			if b[1]-b[0] > b[3]-b[2] {
				f = 4
			}
		}
		kind[uint8(r.ID)] = f
	}
	for i, id := range view.Pixels {
		if id == 0 {
			continue
		}
		c := wallColour
		if id != 255 {
			c = roomPalette[(int(id)-1)%len(roomPalette)]
			if len(o.selected) > 0 && !o.selected[int(id)] {
				c = scale(c, 42)
			}
		}
		x, y := i%view.Width, view.Height-1-i/view.Width // map rows go up, image rows go down
		for dy := 0; dy < mapScale; dy++ {
			for dx := 0; dx < mapScale; dx++ {
				X, Y, k := x*mapScale+dx, y*mapScale+dy, kind[id]
				edge := k == 2 && (X%36 == 0 || Y%36 == 0) || k == 4 && (Y%12 == 0 || (X+(Y/12&1)*24)%48 == 0) || k == 3 && (X%12 == 0 || (Y+(X/12&1)*24)%48 == 0)
				cc := c
				if edge && id != 255 {
					cc = scale(c, 84)
				}
				img.SetRGBA(X, Y, cc)
			}
		}
	}
	// Robot millimetres to image pixels.
	px := func(p []int) (int, int) {
		return (p[0] - view.Origin[0]) * mapScale / view.GridSize, (view.Height*view.GridSize - (p[1] - view.Origin[1])) * mapScale / view.GridSize
	}
	for _, r := range view.Rooms { // room number badge, as the web UI and the app
		x, y := int((r.X+.5)*mapScale), int((float64(view.Height)-r.Y-.5)*mapScale)-9
		if x < 8 || y < 8 || x+8 >= img.Rect.Dx() || y+8 >= img.Rect.Dy() {
			continue // the badge would not fit (a tiny map)
		}
		ring := scale(roomPalette[(r.ID-1)%len(roomPalette)], 100)
		if len(o.selected) > 0 && !o.selected[r.ID] {
			ring = scale(ring, 55)
		}
		disc(img, x, y, 8, ring)
		disc(img, x, y, 6, obstacleRing)
		digits(img, x, y, strconv.Itoa(r.Num))
	}
	if o.base {
		return encodePNG(img)
	}
	drawOverlays(img, view, px)
	live := a.livePose()
	if !live.Live || live.MapID != view.MapID {
		live = livePose{}
	}
	// Floor and walls the robot found this run that the saved map lacks, inside the saved map's frame
	// (the calibration points stay valid), as the web UI draws them.
	for _, c := range live.NewCells {
		x0, y0 := px([]int{c[0], c[1] + live.GridSize})
		x1, y1 := px([]int{c[0] + live.GridSize, c[1]})
		colour := newFloorColour
		if c[2] == 2 {
			colour = newWallColour
		}
		for y := max(y0, 0); y < min(y1, img.Rect.Dy()); y++ {
			for x := max(x0, 0); x < min(x1, img.Rect.Dx()); x++ {
				img.SetRGBA(x, y, colour)
			}
		}
	}
	passes := mopPasses(live.Path, img.Rect.Dx(), img.Rect.Dy(), mopWidth/2*mapScale/view.GridSize, px)
	for i, n := range passes {
		if n == 0 || img.Pix[i*4+3] == 0 { // only the floor gets wet
			continue
		}
		alpha := mopAlpha[min(int(n), len(mopAlpha))-1]
		p := img.Pix[i*4 : i*4+3]
		for c, v := range []uint8{mopColour.R, mopColour.G, mopColour.B} {
			p[c] = uint8((int(p[c])*(255-alpha) + int(v)*alpha) / 255)
		}
	}
	for i := 1; i < len(live.Path); i++ {
		x0, y0 := px(live.Path[i-1])
		x1, y1 := px(live.Path[i])
		line(img, x0, y0, x1, y1, pathColour)
	}
	for _, z := range live.Zones {
		x0, y0 := px(z[:2])
		x1, y1 := px(z[2:4])
		for _, e := range [][4]int{{x0, y0, x1, y0}, {x1, y0, x1, y1}, {x1, y1, x0, y1}, {x0, y1, x0, y0}} {
			line(img, e[0], e[1], e[2], e[3], targetColour)
		}
	}
	for _, s := range live.Spots {
		x, y := px(s)
		disc(img, x, y, 4, targetColour)
	}
	for _, o := range view.Obstacles {
		x, y := px(o[:2])
		c := obstacleColour
		if hazardTypes[o[2]] {
			c = hazardColour
		}
		disc(img, x, y, 6, obstacleRing)
		disc(img, x, y, 4, c)
	}
	charger := view.Charger
	if live.Charger != nil {
		charger = live.Charger
	}
	if charger != nil {
		x, y := px(charger)
		disc(img, x, y, 5, chargerColour)
	}
	if live.Robot != nil {
		x, y := px(live.Robot)
		disc(img, x, y, 7, robotColour)
	}
	return encodePNG(img)
}

func encodePNG(img image.Image) ([]byte, error) {
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func scale(c color.RGBA, percent int) color.RGBA {
	return color.RGBA{uint8(int(c.R) * percent / 100), uint8(int(c.G) * percent / 100), uint8(int(c.B) * percent / 100), 255}
}

func disc(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if x*x+y*y <= r*r {
				img.SetRGBA(cx+x, cy+y, c)
			}
		}
	}
}

// mopPasses counts, per image pixel, how many times the mop pads went over it. Pixels touched
// again within 2 pad widths of travel belong to the same pass (the band of one line overlaps
// itself at every point); coming back after moving further away is another pass.
func mopPasses(path [][]int, w, h, r int, px func([]int) (int, int)) []uint8 {
	count := make([]uint8, w*h)
	last := make([]float64, w*h) // path length in mm when the pixel was last wetted
	travelled := 0.0
	for i := 1; i < len(path); i++ {
		travelled += math.Hypot(float64(path[i][0]-path[i-1][0]), float64(path[i][1]-path[i-1][1]))
		if len(path[i]) < 3 || path[i][2] != 1 {
			continue
		}
		x0, y0 := px(path[i-1])
		x1, y1 := px(path[i])
		stroke(x0, y0, x1, y1, func(cx, cy int) {
			for y := max(cy-r, 0); y <= min(cy+r, h-1); y++ {
				for x := max(cx-r, 0); x <= min(cx+r, w-1); x++ {
					if (x-cx)*(x-cx)+(y-cy)*(y-cy) > r*r {
						continue
					}
					k := y*w + x
					if count[k] == 0 || travelled-last[k] > 2*mopWidth {
						count[k] = min(count[k]+1, 3)
					}
					last[k] = travelled
				}
			}
		})
	}
	return count
}

// line draws with Bresenham's algorithm; SetRGBA ignores points outside the image.
func line(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	stroke(x0, y0, x1, y1, func(x, y int) { img.SetRGBA(x, y, c) })
}

func stroke(x0, y0, x1, y1 int, plot func(x, y int)) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	for e := dx + dy; ; {
		plot(x0, y0)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * e
		if e2 >= dy {
			e += dy
			x0 += sx
		}
		if e2 <= dx {
			e += dx
			y0 += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
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
	points := []map[string]any{}
	for _, p := range [][2]int{{0, 0}, {1000, 0}, {0, 1000}} {
		x := float64(p[0]-view.Origin[0]) * mapScale / float64(view.GridSize)
		y := float64(view.Height*view.GridSize-(p[1]-view.Origin[1])) * mapScale / float64(view.GridSize)
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

// 3x5 digits, 2x scaled, centred on (cx, cy): enough for room numbers without a font.
var digitFont = [10][5]uint8{{7, 5, 5, 5, 7}, {2, 6, 2, 2, 7}, {7, 1, 7, 4, 7}, {7, 1, 7, 1, 7}, {5, 5, 7, 1, 1}, {7, 4, 7, 1, 7}, {7, 4, 7, 5, 7}, {7, 1, 1, 1, 1}, {7, 5, 7, 5, 7}, {7, 5, 7, 1, 7}}

func digits(img *image.RGBA, cx, cy int, s string) {
	x0 := cx - (len(s)*8-2)/2
	for n, ch := range s {
		for row := 0; row < 5; row++ {
			for col := 0; col < 3; col++ {
				if digitFont[ch-'0'][row]>>(2-col)&1 == 1 {
					for dy := 0; dy < 2; dy++ {
						for dx := 0; dx < 2; dx++ {
							img.SetRGBA(x0+n*8+col*2+dx, cy-5+row*2+dy, color.RGBA{255, 255, 255, 255})
						}
					}
				}
			}
		}
	}
}
