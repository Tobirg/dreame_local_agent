// Pet photos. The app's album (AlbumPage, "gif" tab, r2505 plugin) shows short GIFs the robot
// makes when the AI camera sees a pet: node_camera_streamer (CameraGifThread) writes them into
// /data/gifimage/ and the firmware sends them to the cloud, which the app then lists
// (getOssList type "gif"). Without the cloud they only live on the robot until the firmware
// clears the folder, so the agent copies new ones into petPhotoDir and serves them itself.
// Obstacle photos of type 158 ("pet") that keepRunObstacles kept with a run are listed too.
package main

import (
	"context"
	"encoding/json"
	"errors"
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
	firmwarePetDir   = "/data/gifimage"
	petPhotoDir      = "/data/dreame-local/pet-photos"
	petPhotoIndex    = "/data/dreame-local/pet-photos.json" // names the user deleted, so they are not copied again
	maxPetPhotos     = 200
	maxPetPhotoBytes = 8 << 20  // one file
	maxPetPhotoTotal = 96 << 20 // all kept files; the oldest go first
	petObstacleType  = 158
	petPhotoSettle   = 15 * time.Second // a file younger than this may still be being written
)

// Kept names: "pet-<unix time of the firmware file>-<its name, other characters replaced>.<ext>".
var (
	petPhotoName   = regexp.MustCompile(`^pet-[0-9]{1,12}-[A-Za-z0-9_.-]{1,80}\.(gif|jpg|png)$`)
	petUnsafeChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)
	petPhotoMu     sync.Mutex
)

type petPhoto struct {
	Name   string    `json:"name"`
	Source string    `json:"source"` // "gif": the robot's pet GIF; "obstacle": a run's obstacle photo of a pet
	At     time.Time `json:"at"`
	Size   int64     `json:"size,omitempty"`
	URL    string    `json:"url"` // GET with the token
	// Obstacle photos only: confidence in % and the run they belong to.
	Confidence int        `json:"confidence,omitempty"`
	Run        *time.Time `json:"run,omitempty"`
}

func petContentType(name string) string {
	switch filepath.Ext(name) {
	case ".gif":
		return "image/gif"
	case ".png":
		return "image/png"
	}
	return "image/jpeg"
}

func (a *agent) runPetPhotos(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		a.collectPetPhotos(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *agent) deletedPetPhotos() map[string]bool {
	var index struct {
		Deleted []string `json:"deleted"`
	}
	if b, err := readLimited(a.path(petPhotoIndex), 1<<20); err == nil {
		_ = json.Unmarshal(b, &index)
	}
	out := map[string]bool{}
	for _, n := range index.Deleted {
		out[n] = true
	}
	return out
}

// collectPetPhotos copies the firmware's new pet pictures and trims the kept ones to the limits.
func (a *agent) collectPetPhotos(now time.Time) {
	entries, err := os.ReadDir(a.path(firmwarePetDir))
	if err != nil {
		return // no folder: the robot has not made any yet
	}
	petPhotoMu.Lock()
	defer petPhotoMu.Unlock()
	deleted := a.deletedPetPhotos()
	copied := false
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".jpeg" {
			ext = ".jpg"
		}
		if !e.Type().IsRegular() || (ext != ".gif" && ext != ".jpg" && ext != ".png") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() == 0 || info.Size() > maxPetPhotoBytes || now.Sub(info.ModTime()) < petPhotoSettle {
			continue
		}
		base := petUnsafeChars.ReplaceAllString(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())), "_")
		if len(base) > 80 {
			base = base[:80]
		}
		name := fmt.Sprintf("pet-%d-%s%s", info.ModTime().Unix(), base, ext)
		dest := filepath.Join(a.path(petPhotoDir), name)
		if !petPhotoName.MatchString(name) || deleted[name] {
			continue
		}
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		b, err := readLimited(filepath.Join(a.path(firmwarePetDir), e.Name()), maxPetPhotoBytes)
		if err != nil || os.MkdirAll(a.path(petPhotoDir), 0700) != nil {
			continue
		}
		if atomicPrivate(dest, b) == nil {
			_ = os.Chtimes(dest, info.ModTime(), info.ModTime())
			copied = true
		}
	}
	if copied {
		a.trimPetPhotos()
	}
}

// keptPetPhotos lists the copied files, newest first.
func (a *agent) keptPetPhotos() []petPhoto {
	entries, _ := os.ReadDir(a.path(petPhotoDir))
	out := []petPhoto{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !petPhotoName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, petPhoto{Name: e.Name(), Source: "gif", At: info.ModTime().UTC(), Size: info.Size(),
			URL: "/api/v1/pets/photo?name=" + e.Name()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (a *agent) trimPetPhotos() {
	var total int64
	removed := []string{}
	for i, p := range a.keptPetPhotos() {
		total += p.Size
		if i >= maxPetPhotos || total > maxPetPhotoTotal {
			if os.Remove(filepath.Join(a.path(petPhotoDir), p.Name)) == nil {
				removed = append(removed, p.Name)
			}
		}
	}
	if len(removed) > 0 {
		_ = a.forgetPetPhotos(removed...)
	}
}

// forgetPetPhotos remembers removed copies so they are not copied again while the firmware still has
// the file; names whose firmware file is gone are dropped from the list, they cannot come back.
func (a *agent) forgetPetPhotos(names ...string) error {
	deleted := a.deletedPetPhotos()
	for _, n := range names {
		deleted[n] = true
	}
	present := map[string]bool{}
	if entries, err := os.ReadDir(a.path(firmwarePetDir)); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				present[fmt.Sprint(info.ModTime().Unix())] = true
			}
		}
	}
	keep := []string{}
	for n := range deleted {
		if stamp, _, _ := strings.Cut(strings.TrimPrefix(n, "pet-"), "-"); present[stamp] {
			keep = append(keep, n)
		}
	}
	sort.Strings(keep)
	b, _ := json.Marshal(map[string][]string{"deleted": keep})
	if err := os.MkdirAll(filepath.Dir(a.path(petPhotoIndex)), 0700); err != nil {
		return err
	}
	return atomicPrivate(a.path(petPhotoIndex), b)
}

// petPhotos lists the GIFs and the pet obstacle photos of the cleaning history, newest first.
func (a *agent) petPhotos() []petPhoto {
	petPhotoMu.Lock()
	out := a.keptPetPhotos()
	petPhotoMu.Unlock()
	a.dev.mu.Lock()
	runs := a.readHistory()
	a.dev.mu.Unlock()
	for _, r := range runs {
		for i, o := range r.Obstacles {
			if len(o) < 3 || o[2] != petObstacleType || i >= len(r.Photos) || r.Photos[i] == "" {
				continue
			}
			start, p := r.Start, petPhoto{Name: r.Photos[i], Source: "obstacle", At: r.Start.UTC(),
				URL: "/api/v1/history/photo?name=" + r.Photos[i]}
			if r.End != nil {
				p.At = r.End.UTC()
			}
			if len(o) > 3 {
				p.Confidence = o[3]
			}
			p.Run = &start
			if info, err := os.Stat(filepath.Join(a.path(runPhotoDir), r.Photos[i])); err == nil {
				p.Size = info.Size()
				out = append(out, p)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (a *agent) petPhotoFile(name string) ([]byte, error) {
	if !petPhotoName.MatchString(name) {
		return nil, errors.New("invalid photo name")
	}
	return readLimited(filepath.Join(a.path(petPhotoDir), name), maxPetPhotoBytes)
}

// deletePetPhoto removes a kept GIF (and remembers it, so it is not copied again while the
// firmware still has it) or a pet photo of a run (the run keeps the obstacle, without the photo).
func (a *agent) deletePetPhoto(name string) error {
	switch {
	case petPhotoName.MatchString(name):
		petPhotoMu.Lock()
		defer petPhotoMu.Unlock()
		if err := os.Remove(filepath.Join(a.path(petPhotoDir), name)); err != nil {
			return errors.New("photo not found")
		}
		return a.forgetPetPhotos(name)
	case runPhotoName.MatchString(name):
		a.dev.mu.Lock()
		defer a.dev.mu.Unlock()
		runs, found := a.readHistory(), false
		for r := range runs {
			for i, p := range runs[r].Photos {
				if p == name && i < len(runs[r].Obstacles) && len(runs[r].Obstacles[i]) > 2 && runs[r].Obstacles[i][2] == petObstacleType {
					runs[r].Photos[i], found = "", true
				}
			}
		}
		if !found {
			return errors.New("photo not found")
		}
		b, err := json.Marshal(runs)
		if err == nil {
			err = atomicPrivate(a.path(historyFile), b)
		}
		if err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(a.path(runPhotoDir), name))
		return nil
	}
	return errors.New("invalid photo name")
}
