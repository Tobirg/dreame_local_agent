// Obstacles. The AI camera's finds come in the map frames as "ai_obstacle" (r2505 4.3.9_2021:
// seen in the run's final frame that fds_hook.sh keeps in /data/log/fds_log/<time> for the cloud).
// Each list replaces the previous one. The firmware's copies rotate away (the IoT log keeps about
// an hour), so the agent keeps the newest list it has seen in obstaclesFile.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	fdsLogDir     = "/data/log/fds_log"
	obstaclesFile = "/data/dreame-local/obstacles.json"
)

type obstacleList struct {
	MapID int       `json:"map_id"`
	At    time.Time `json:"at"`
	List  [][]int   `json:"obstacles"` // [x, y, type, confidence %]
}

var (
	fdsFramesMu sync.Mutex
	fdsFrames   = map[string]liveFrame{} // the firmware never rewrites a run's file
)

// fdsLogFrame returns the newest run file that reports obstacles; runs without the key are skipped.
func (a *agent) fdsLogFrame() (liveFrame, bool) {
	entries, _ := os.ReadDir(a.path(fdsLogDir))
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() }) // named by time, newest first
	fdsFramesMu.Lock()
	defer fdsFramesMu.Unlock()
	for _, e := range entries {
		file := filepath.Join(a.path(fdsLogDir), e.Name())
		f, ok := fdsFrames[file]
		if !ok {
			if b, err := readLimited(file, maxMap); err == nil {
				f, _ = parseLiveFrame(string(b))
				f.cells, f.path = nil, ""
			}
			fdsFrames[file] = f
		}
		if f.obstacles != nil {
			return f, true
		}
	}
	return liveFrame{}, false
}

// obstacles returns the newest obstacle list of the given saved map (empty when none is known).
func (a *agent) obstacles(mapID int) obstacleList {
	var frames []liveFrame
	if f, ok := a.fdsLogFrame(); ok {
		frames = append(frames, f)
	}
	for _, file := range liveFrameFiles {
		if b, err := readLimited(a.path(file), maxMap); err == nil {
			if f, ok := parseLiveFrame(string(b)); ok {
				frames = append(frames, f)
			}
		}
	}
	frames = append(frames, a.iotLogFrames()...)
	var kept obstacleList
	if b, err := readLimited(a.path(obstaclesFile), 1<<20); err == nil {
		_ = json.Unmarshal(b, &kept)
	}
	newest := kept
	for _, f := range frames {
		if f.obstacles != nil && f.at.After(newest.At) {
			newest = obstacleList{MapID: f.mapID, At: f.at.UTC(), List: f.obstacles}
		}
	}
	if newest.At.After(kept.At) {
		if b, err := json.Marshal(newest); err == nil && os.MkdirAll(filepath.Dir(a.path(obstaclesFile)), 0700) == nil {
			_ = atomicPrivate(a.path(obstaclesFile), b)
		}
	}
	if newest.MapID != mapID || newest.List == nil {
		return obstacleList{MapID: mapID, List: [][]int{}}
	}
	return newest
}
