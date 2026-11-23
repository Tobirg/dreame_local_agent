// Live position. Observed on r2505 4.3.9_2021 during a room cleaning:
//   - when a run starts the firmware writes full map frames (FRAME_TYPE=I) to
//     /tmp/log/fds_tmp_file_0 and _1 for its cloud upload (fds_hook.sh, /data/log/fds.log);
//   - while the robot moves it sends a P frame every ~3 s as property 6/1 and logs each one,
//     in full, to /data/log/ava_iot.log ("msgcvt>iot] ... properties_changed").
//
// Both are the saved-map base64/zlib frame. The metadata carries the robot and station
// position and "tr", the piece of path since the previous frame; frames of one run share "mtid".
package main

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	liveFrameMaxAge = 90 * time.Second
	iotLogFile      = "/data/log/ava_iot.log"
)

var (
	liveFrameFiles = []string{"/tmp/log/fds_tmp_file_0", "/tmp/log/fds_tmp_file_1"}
	mapPropValue   = regexp.MustCompile(`"siid":6,"piid":1,"value":"([A-Za-z0-9_+/=-]+)"`)
)

type livePose struct {
	Live      bool       `json:"live"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	MapID     int        `json:"map_id,omitempty"`
	Robot     []int      `json:"robot,omitempty"`
	Angle     *int       `json:"angle,omitempty"`
	Charger   []int      `json:"charger,omitempty"`
	Path      [][]int    `json:"path,omitempty"`  // [x, y, mop]: mop 1 where the robot mopped
	Spots     [][]int    `json:"spots,omitempty"` // spot cleaning points [x, y]; the robot picks the area
	Zones     [][]int    `json:"zones,omitempty"` // zone cleaning rectangles [x0, y0, x1, y1]
	// Cells the run's own map has but the saved map lacks: [x, y, kind], the cell's lower-left corner
	// in robot millimetres, kind 1 floor, 2 wall; cell size is grid_size. See liveCells.
	NewCells [][]int `json:"new_cells,omitempty"`
	GridSize int     `json:"grid_size,omitempty"`
}

type liveFrame struct {
	at             time.Time
	mapID, angle   int
	robot, charger []int
	path, run      string
	full           bool // I frame
	spots, zones   [][]int
	obstacles      [][]int // [x, y, type, confidence %]; nil when the frame has no "ai_obstacle"
	// The frame's own grid: header map id and frame number (per run), cell size, origin and size
	// in cells, and the cells: an I frame's are values, a P frame's are changes to add (mod 256).
	run16, seq   int
	grid, ox, oy int
	w, h         int
	cells        []byte
}

func parseLiveFrame(encoded string) (liveFrame, bool) {
	raw, err := decodeFrame(encoded)
	if err != nil {
		return liveFrame{}, false
	}
	f := liveFrame{full: raw[4] == 'I', mapID: int(binary.LittleEndian.Uint16(raw[0:2])), angle: int(int16(binary.LittleEndian.Uint16(raw[9:11]))),
		// header pose: int16 x, y at bytes 5..8, used when the metadata has none
		robot: []int{int(int16(binary.LittleEndian.Uint16(raw[5:7]))), int(int16(binary.LittleEndian.Uint16(raw[7:9])))}}
	w, h := int(binary.LittleEndian.Uint16(raw[19:21])), int(binary.LittleEndian.Uint16(raw[21:23]))
	f.run16, f.seq, f.grid, f.w, f.h = f.mapID, int(binary.LittleEndian.Uint16(raw[2:4])), int(binary.LittleEndian.Uint16(raw[17:19])), w, h
	f.ox, f.oy = int(int16(binary.LittleEndian.Uint16(raw[23:25]))), int(int16(binary.LittleEndian.Uint16(raw[25:27])))
	var meta struct {
		Robot   json.RawMessage `json:"robot"`
		Charger json.RawMessage `json:"charger"`
		Path    string          `json:"tr"`
		Run     string          `json:"mtid"`
		TimeMS  int64           `json:"timestamp_ms"`
		// Saved map id ("2"); the header id is a per-run counter and may differ from it.
		Map json.Number `json:"curid"`
		// Spot cleaning: [[x, y, repeats, suction, water]].
		Spots json.RawMessage `json:"sp"`
		// Zone cleaning: {"areas": [[x0, y0, x1, y1, repeats, suction, water, ?]]}.
		Zones json.RawMessage `json:"da2"`
		// Recognised objects: [["x", "y", "type", "confidence 0..1", time, "/data/record/ai_image/1.jpg", …]], all strings.
		Obstacles json.RawMessage `json:"ai_obstacle"`
	}
	if 27+w*h > len(raw) || json.Unmarshal(raw[27+w*h:], &meta) != nil || meta.TimeMS == 0 {
		return liveFrame{}, false
	}
	f.cells = raw[27 : 27+w*h]
	if p := point(meta.Robot); p != nil {
		f.robot = p
	}
	var spots [][]int
	_ = json.Unmarshal(meta.Spots, &spots) // an odd value must not drop the frame
	for _, s := range spots {
		if len(s) >= 2 {
			f.spots = append(f.spots, s[:2])
		}
	}
	var zones struct {
		Areas [][]int `json:"areas"`
	}
	_ = json.Unmarshal(meta.Zones, &zones)
	for _, z := range zones.Areas {
		if len(z) >= 4 {
			f.zones = append(f.zones, z[:4])
		}
	}
	var obstacles [][]any
	if meta.Obstacles != nil && json.Unmarshal(meta.Obstacles, &obstacles) == nil {
		f.obstacles = [][]int{}
	}
	num := func(v any) (float64, error) { s, _ := v.(string); return strconv.ParseFloat(s, 64) }
	for _, o := range obstacles {
		if len(o) < 4 {
			continue
		}
		x, errX := num(o[0])
		y, errY := num(o[1])
		kind, errK := num(o[2])
		p, errP := num(o[3])
		if errX == nil && errY == nil && errK == nil && errP == nil {
			f.obstacles = append(f.obstacles, []int{int(x), int(y), int(kind), int(p*100 + 0.5)})
		}
	}
	if id, err := meta.Map.Int64(); err == nil {
		f.mapID = int(id)
	}
	f.charger, f.path, f.run, f.at = point(meta.Charger), meta.Path, meta.Run, time.UnixMilli(meta.TimeMS)
	return f, true
}

type logFrames struct {
	size   int64
	mod    time.Time
	frames []liveFrame
}

var (
	logFramesMu    sync.Mutex
	logFramesCache = map[string]logFrames{} // rotated logs never change, so they are parsed once
)

// iotLogFrames reads the P frames of the IoT log and its rotated copies (ava_iot-<date>.log,
// ~1 MB each, about half an hour of cleaning), so a long run keeps its early path.
func (a *agent) iotLogFrames() []liveFrame {
	rotated, _ := filepath.Glob(a.path(strings.TrimSuffix(iotLogFile, ".log") + "-*.log"))
	logFramesMu.Lock()
	defer logFramesMu.Unlock()
	var frames []liveFrame
	seen := map[string]bool{}
	for _, file := range append(rotated, a.path(iotLogFile)) {
		st, err := os.Stat(file)
		if err != nil {
			continue
		}
		seen[file] = true
		c, ok := logFramesCache[file]
		if !ok || c.size != st.Size() || !c.mod.Equal(st.ModTime()) {
			c = logFrames{size: st.Size(), mod: st.ModTime()}
			if b, err := readLimited(file, 8<<20); err == nil {
				for _, m := range mapPropValue.FindAllSubmatch(b, -1) {
					if f, ok := parseLiveFrame(string(m[1])); ok {
						c.frames = append(c.frames, f)
					}
				}
			}
			logFramesCache[file] = c
		}
		frames = append(frames, c.frames...)
	}
	for file := range logFramesCache {
		if !seen[file] {
			delete(logFramesCache, file)
		}
	}
	return frames
}

// livePose returns {"live": false} when the robot sent no frame recently (not cleaning).
func (a *agent) livePose() livePose {
	var frames []liveFrame
	for _, file := range liveFrameFiles {
		if b, err := readLimited(a.path(file), maxMap); err == nil {
			if f, ok := parseLiveFrame(string(b)); ok {
				frames = append(frames, f)
			}
		}
	}
	frames = append(frames, a.iotLogFrames()...)
	if len(frames) == 0 {
		return livePose{}
	}
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].at.Before(frames[j].at) })
	last := frames[len(frames)-1]
	if time.Since(last.at) > liveFrameMaxAge {
		return livePose{}
	}
	pose := livePose{Live: true, UpdatedAt: &last.at, MapID: last.mapID, Robot: last.robot, Angle: &last.angle, Spots: last.spots, Zones: last.zones}
	mop := 0
	for _, f := range frames {
		if f.run != last.run {
			continue
		}
		if f.full { // a run starts with I frames; the first one still carries the previous run's mtid
			pose.Path, mop = nil, 0
		}
		var points [][]int
		points, mop = parsePath(f.path, mop)
		pose.Path = append(pose.Path, points...)
		if f.charger != nil && f.charger[0] != 32767 { // 32767: not known in this frame
			pose.Charger = f.charger
		}
	}
	if view, err := a.mapView(); err == nil && !view.Empty && len(view.Origin) == 2 {
		pose.NewCells, pose.GridSize = liveCells(frames, last.run16, view), view.GridSize
	}
	return pose
}

const maxNewCells = 20000

// liveCells rebuilds the run's grid (its newest I frame plus the later P frames, measured on r2505:
// a P frame's cells are differences, value = (old + change) & 0xff) and returns the cells the saved
// map does not have. Run grid cells are room<<2 | flags, 252 (room 63) a wall; saved map 0 is nothing.
func liveCells(frames []liveFrame, run16 int, view mapView) [][]int {
	var base *liveFrame
	for i := range frames {
		f := &frames[i]
		if f.full && f.run16 == run16 && f.cells != nil && (base == nil || f.seq > base.seq) {
			base = f
		}
	}
	if base == nil || base.grid != view.GridSize || base.grid <= 0 {
		return nil
	}
	g := base.grid
	type cell struct{ x, y int }
	grid := map[cell]byte{}
	put := func(f *liveFrame, add bool) {
		for y := 0; y < f.h; y++ {
			for x := 0; x < f.w; x++ {
				c := cell{f.ox/g + x, f.oy/g + y}
				if v := f.cells[y*f.w+x]; add {
					grid[c] += v
				} else {
					grid[c] = v
				}
			}
		}
	}
	put(base, false)
	later := []*liveFrame{}
	for i := range frames {
		if f := &frames[i]; !f.full && f.run16 == run16 && f.seq > base.seq && f.grid == g && f.cells != nil {
			later = append(later, f)
		}
	}
	sort.SliceStable(later, func(i, j int) bool { return later[i].seq < later[j].seq })
	for _, f := range later {
		put(f, true)
	}
	saved := func(c cell) byte {
		x, y := c.x-view.Origin[0]/g, c.y-view.Origin[1]/g
		if x < 0 || y < 0 || x >= view.Width || y >= view.Height || len(view.Pixels) != view.Width*view.Height {
			return 0
		}
		return view.Pixels[y*view.Width+x]
	}
	out := [][]int{}
	for c, v := range grid {
		if v == 0 || saved(c) != 0 {
			continue
		}
		kind := 1
		if v>>2 == 63 {
			kind = 2
		}
		if out = append(out, []int{c.x * g, c.y * g, kind}); len(out) >= maxNewCells {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][1] < out[j][1] || out[i][1] == out[j][1] && out[i][0] < out[j][0] })
	return out
}

// point reads [x, y]; I frames write it as a string, "[5,455]", P frames as an array.
func point(raw json.RawMessage) []int {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var p []int
	if json.Unmarshal(raw, &p) != nil || len(p) != 2 {
		return nil
	}
	return p
}

var pathStep = regexp.MustCompile(`([A-Za-z])(-?\d+),(-?\d+)`)

// parsePath reads one frame's "tr" into [x, y, mop] points. "L" points are offsets from the
// previous point; any other letter starts at an absolute point. S, W and M also set the mode of
// what follows until the next of them (checked on r2505: a vacuum-only run starts with "S" and
// has moptype 0, mopping runs use "W"); "l" keeps it. mop is 1 for W (vacuum and mop) and M (mop).
func parsePath(tr string, mop int) ([][]int, int) {
	var out [][]int
	x, y := 0, 0
	for _, m := range pathStep.FindAllStringSubmatch(tr, 20000) {
		dx, _ := strconv.Atoi(m[2])
		dy, _ := strconv.Atoi(m[3])
		switch m[1] {
		case "L":
			x, y = x+dx, y+dy
		case "W", "M":
			x, y, mop = dx, dy, 1
		case "S":
			x, y, mop = dx, dy, 0
		default:
			x, y = dx, dy
		}
		out = append(out, []int{x, y, mop})
	}
	return out, mop
}
