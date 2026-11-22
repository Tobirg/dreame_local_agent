// Live position. While cleaning, the firmware writes full map frames (FRAME_TYPE=I) to
// /tmp/log/fds_tmp_file_0 and _1 and hands them to fds_hook.sh for the cloud (seen in
// /data/log/fds.log on r2505). The agent only reads the newest one: same base64/zlib frame
// as the saved map, with the robot and station position and the cleaned path in its metadata.
package main

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"time"
)

const liveFrameMaxAge = 90 * time.Second

var liveFrameFiles = []string{"/tmp/log/fds_tmp_file_0", "/tmp/log/fds_tmp_file_1"}

type livePose struct {
	Live      bool       `json:"live"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	MapID     int        `json:"map_id,omitempty"`
	Robot     []int      `json:"robot,omitempty"`
	Angle     *int       `json:"angle,omitempty"`
	Charger   []int      `json:"charger,omitempty"`
	Path      [][]int    `json:"path,omitempty"`
}

// livePose returns {"live": false} when no frame was written recently (not cleaning).
func (a *agent) livePose() livePose {
	var newest string
	var at time.Time
	for _, f := range liveFrameFiles {
		if st, err := os.Stat(a.path(f)); err == nil && st.ModTime().After(at) {
			newest, at = f, st.ModTime()
		}
	}
	if newest == "" || time.Since(at) > liveFrameMaxAge {
		return livePose{}
	}
	b, err := readLimited(a.path(newest), maxMap)
	if err != nil {
		return livePose{}
	}
	raw, err := decodeSavedFrame(string(b))
	if err != nil {
		return livePose{}
	}
	pose := livePose{Live: true, UpdatedAt: &at, MapID: int(binary.LittleEndian.Uint16(raw[0:2]))}
	w, h := int(binary.LittleEndian.Uint16(raw[19:21])), int(binary.LittleEndian.Uint16(raw[21:23]))
	angle := int(int16(binary.LittleEndian.Uint16(raw[9:11])))
	pose.Angle = &angle
	var meta struct {
		Robot   []int  `json:"robot"`
		Charger []int  `json:"charger"`
		Path    string `json:"tr"`
	}
	if 27+w*h <= len(raw) && json.Unmarshal(raw[27+w*h:], &meta) == nil {
		if len(meta.Robot) == 2 {
			pose.Robot = meta.Robot
		}
		if len(meta.Charger) == 2 {
			pose.Charger = meta.Charger
		}
		pose.Path = parsePath(meta.Path)
	}
	if pose.Robot == nil { // header pose: int16 x, y at bytes 5..8
		pose.Robot = []int{int(int16(binary.LittleEndian.Uint16(raw[5:7]))), int(int16(binary.LittleEndian.Uint16(raw[7:9])))}
	}
	return pose
}

var pathStep = regexp.MustCompile(`([MLml])(-?\d+),(-?\d+)`)

// parsePath reads the SVG-like "tr" path: M/L absolute points, m/l relative to the previous one.
// shortcut: other commands (seen in other models' docs as S/W for path kinds) are skipped; check on a real r2505 run.
func parsePath(tr string) [][]int {
	var out [][]int
	x, y := 0, 0
	for _, m := range pathStep.FindAllStringSubmatch(tr, 20000) {
		dx, _ := strconv.Atoi(m[2])
		dy, _ := strconv.Atoi(m[3])
		if m[1] == "M" || m[1] == "L" {
			x, y = dx, dy
		} else {
			x, y = x+dx, y+dy
		}
		out = append(out, []int{x, y})
	}
	return out
}
