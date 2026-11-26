// Obstacles. The AI camera's finds come in the map frames as "ai_obstacle" (r2505 4.3.9_2021:
// seen in the run's final frame that fds_hook.sh keeps in /data/log/fds_log/<time> for the cloud).
// Each list replaces the previous one. The firmware's copies rotate away (the IoT log keeps about
// an hour), so the agent keeps the newest list it has seen in obstaclesFile.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	// Firmware photo path per obstacle (r2505: /data/record/ai_image/N.jpg). The firmware deletes
	// them within hours, so a finished run copies them into its history entry; see keepRunObstacles.
	Photos []string `json:"photos,omitempty"`
}

const (
	firmwarePhotoDir = "/data/record/ai_image/"
	runPhotoDir      = "/data/dreame-local/obstacle-photos"
	runsWithPhotos   = 20 // shortcut: photos of older runs are deleted; raise if the flash allows
)

var runPhotoName = regexp.MustCompile(`^[0-9]+-[0-9]+\.jpg$`)

// keepRunObstacles stores the run's obstacles and copies their photos into its history entry.
// It runs after the run was recorded; the run's final frame is written before the agent sees the end.
func (a *agent) keepRunObstacles(start, end time.Time) {
	o := a.newestObstacles()
	if o.List == nil || o.At.Before(start) || o.At.After(end.Add(time.Minute)) {
		return // no list from this run
	}
	_ = os.MkdirAll(a.path(runPhotoDir), 0700)
	photos := make([]string, len(o.List))
	for i := range o.List {
		if i >= len(o.Photos) {
			break
		}
		src := filepath.Clean(o.Photos[i])
		if !strings.HasPrefix(src, firmwarePhotoDir) || filepath.Ext(src) != ".jpg" {
			continue // "0" on entries without a photo; anything else outside the firmware's folder is ignored
		}
		b, err := readLimited(a.path(src), 4<<20)
		name := fmt.Sprintf("%d-%d.jpg", start.Unix(), i)
		if err == nil && atomicPrivate(filepath.Join(a.path(runPhotoDir), name), b) == nil {
			photos[i] = name
		}
	}
	a.dev.mu.Lock()
	defer a.dev.mu.Unlock()
	runs := a.readHistory()
	keep := map[string]bool{}
	for i := range runs {
		if runs[i].Start.Equal(start) {
			runs[i].Obstacles, runs[i].Photos = o.List, photos
		}
		if i < runsWithPhotos {
			for _, p := range runs[i].Photos {
				keep[p] = true
			}
		} else {
			runs[i].Photos = nil
		}
	}
	if b, err := json.Marshal(runs); err == nil {
		_ = atomicPrivate(a.path(historyFile), b)
	}
	entries, _ := os.ReadDir(a.path(runPhotoDir))
	for _, e := range entries {
		if !keep[e.Name()] {
			_ = os.Remove(filepath.Join(a.path(runPhotoDir), e.Name()))
		}
	}
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
			b, err := readLimited(file, maxMap)
			if err != nil {
				continue
			}
			if f, ok = parseLiveFrame(string(b)); !ok {
				continue // may still be being written; read again next time
			}
			f.cells, f.path = nil, ""
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
	newest := a.newestObstacles()
	if newest.MapID != mapID || newest.List == nil {
		return obstacleList{MapID: mapID, List: [][]int{}}
	}
	return newest
}

// newestObstacles returns the newest list of any map and keeps it in obstaclesFile.
func (a *agent) newestObstacles() obstacleList {
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
			newest = obstacleList{MapID: f.mapID, At: f.at.UTC(), List: f.obstacles, Photos: f.photos}
		}
	}
	if newest.At.After(kept.At) {
		if b, err := json.Marshal(newest); err == nil && os.MkdirAll(filepath.Dir(a.path(obstaclesFile)), 0700) == nil {
			_ = atomicPrivate(a.path(obstaclesFile), b)
		}
	}
	return newest
}
