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
	Path      [][]int    `json:"path,omitempty"`
}

type liveFrame struct {
	at             time.Time
	mapID, angle   int
	robot, charger []int
	path, run      string
	full           bool // I frame
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
	var meta struct {
		Robot   json.RawMessage `json:"robot"`
		Charger json.RawMessage `json:"charger"`
		Path    string          `json:"tr"`
		Run     string          `json:"mtid"`
		TimeMS  int64           `json:"timestamp_ms"`
		// Saved map id ("2"); the header id is a per-run counter and may differ from it.
		Map json.Number `json:"curid"`
	}
	if 27+w*h > len(raw) || json.Unmarshal(raw[27+w*h:], &meta) != nil || meta.TimeMS == 0 {
		return liveFrame{}, false
	}
	if p := point(meta.Robot); p != nil {
		f.robot = p
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
	pose := livePose{Live: true, UpdatedAt: &last.at, MapID: last.mapID, Robot: last.robot, Angle: &last.angle}
	for _, f := range frames {
		if f.run != last.run {
			continue
		}
		if f.full { // a run starts with I frames; the first one still carries the previous run's mtid
			pose.Path = nil
		}
		pose.Path = append(pose.Path, parsePath(f.path)...)
		if f.charger != nil && f.charger[0] != 32767 { // 32767: not known in this frame
			pose.Charger = f.charger
		}
	}
	return pose
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

// parsePath reads one frame's "tr": "L" points are offsets from the previous point, any other
// letter ("l", "W") starts at an absolute point (seen on r2505: "l-2942,-1623L51,0", "W-1687,-653").
// shortcut: the letter is the segment kind (W: sweep and mop, per the Tasshack decoder) and is
// not shown yet; its meaning on r2505 needs a run without mopping to compare.
func parsePath(tr string) [][]int {
	var out [][]int
	x, y := 0, 0
	for _, m := range pathStep.FindAllStringSubmatch(tr, 20000) {
		dx, _ := strconv.Atoi(m[2])
		dy, _ := strconv.Atoi(m[3])
		if m[1] == "L" {
			x, y = x+dx, y+dy
		} else {
			x, y = dx, dy
		}
		out = append(out, []int{x, y})
	}
	return out
}
