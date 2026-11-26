package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Map backups without the cloud. The firmware's own backup (action 6/3) tars a staging folder
// and uploads it; the package holds <id>/ = /data/map/<id>, <id>.dat2 = /data/ri/<id>.dat2,
// ai/<id> = /data/DivideAI/ai_result/<id> and divide/<id> = /data/DivideMap/<id> (compared
// file by file on r2505 2026-11-24). The agent builds the same package itself and restores it the
// way the app does: property 6/10 {"map_id", "map_url"}; msg_cvt.sh downloads the URL with wget
// into /data/multmap_recover, and 6/11 reports the restore state (3 = done in the app).

const (
	mapBackupDir   = "/data/dreame-local/map-backups"
	mapBackupsKept = 10
	restoreURLLife = 5 * time.Minute
)

var backupNameRe = regexp.MustCompile(`^map([0-9]{1,4})-([0-9]{10})\.tbz2$`)

type mapBackup struct {
	Name  string `json:"name"`
	MapID int    `json:"map_id"`
	Time  int64  `json:"time"`
	Size  int64  `json:"size"`
}

var restoreOffer struct {
	sync.Mutex
	key, file string
	until     time.Time
}

func (a *agent) mapBackups() []mapBackup {
	entries, _ := os.ReadDir(a.path(mapBackupDir))
	out := []mapBackup{}
	for _, e := range entries {
		m := backupNameRe.FindStringSubmatch(e.Name())
		info, err := e.Info()
		if m == nil || err != nil {
			continue
		}
		id, _ := strconv.Atoi(m[1])
		t, _ := strconv.ParseInt(m[2], 10, 64)
		out = append(out, mapBackup{e.Name(), id, t, info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out
}

// createMapBackup packs the current map's files; the oldest backups over mapBackupsKept go.
func (a *agent) createMapBackup() (mapBackup, error) {
	view, err := a.mapView()
	if err != nil {
		return mapBackup{}, err
	}
	if view.Empty {
		return mapBackup{}, errors.New("no saved map")
	}
	id := strconv.Itoa(view.MapID)
	if err := os.MkdirAll(a.path(mapBackupDir), 0700); err != nil {
		return mapBackup{}, err
	}
	stage, err := os.MkdirTemp(a.path(mapBackupDir), ".stage-")
	if err != nil {
		return mapBackup{}, err
	}
	defer os.RemoveAll(stage)
	parts := []struct{ from, to string }{
		{"/data/map/" + id, id}, {"/data/ri/" + id + ".dat2", id + ".dat2"},
		{"/data/DivideAI/ai_result/" + id, "ai/" + id}, {"/data/DivideMap/" + id, "divide/" + id},
	}
	for _, p := range parts {
		if err := copyTree(a.path(p.from), filepath.Join(stage, p.to)); err != nil {
			return mapBackup{}, fmt.Errorf("cannot copy %s: %w", p.from, err)
		}
	}
	name := fmt.Sprintf("map%s-%d.tbz2", id, time.Now().Unix())
	final := filepath.Join(a.path(mapBackupDir), name)
	// Entries without "./", as the firmware's own "cd dir; tar -jcf x *" writes them.
	if out, err := exec.Command("tar", "-jcf", final+".tmp", "-C", stage, id, id+".dat2", "ai", "divide").CombinedOutput(); err != nil {
		_ = os.Remove(final + ".tmp")
		return mapBackup{}, fmt.Errorf("tar failed: %v %s", err, strings.TrimSpace(string(out)))
	}
	// The firmware's restore clears the per-room settings and order; keep them beside the package.
	if rooms := a.roomSettings(view.MapID); rooms != nil {
		b, _ := json.Marshal(rooms)
		if err := os.WriteFile(final+".json", b, 0600); err != nil {
			return mapBackup{}, err
		}
	}
	if err := os.Rename(final+".tmp", final); err != nil {
		return mapBackup{}, err
	}
	all := a.mapBackups()
	for _, old := range all[min(len(all), mapBackupsKept):] {
		a.removeMapBackup(old.Name)
	}
	for _, b := range all {
		if b.Name == name {
			return b, nil
		}
	}
	return mapBackup{}, errors.New("backup was not kept")
}

// savedMaps reads the robot's map list: /data/ri/config.json2 {"order": [ids], "curr_id": id}.
func (a *agent) savedMaps() map[string]any {
	var c struct {
		Order []int `json:"order"`
		Curr  int   `json:"curr_id"`
	}
	b, err := readLimited(a.path("/data/ri/config.json2"), 4096)
	if err != nil || json.Unmarshal(b, &c) != nil {
		return nil
	}
	return map[string]any{"ids": c.Order, "current": c.Curr}
}

// useMapEdit switches the current map like the app's LDSMapListPage (onUseMap).
func (a *agent) useMapEdit(raw json.RawMessage) (map[string]any, error) {
	var p struct {
		MapID int `json:"map_id"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil {
		return nil, errors.New("expected {\"map_id\": id}")
	}
	maps := a.savedMaps()
	ids, _ := maps["ids"].([]int)
	for _, id := range ids {
		if id == p.MapID {
			a.dev.mu.Lock()
			activity, _ := a.dev.snap.State["activity"].(string)
			a.dev.mu.Unlock()
			if activity != "" && activity != "docked" && activity != "idle" {
				return nil, errors.New("stop the robot before switching maps")
			}
			return map[string]any{"sm": map[string]any{}, "mapid": p.MapID}, nil
		}
	}
	return nil, errors.New("no such saved map")
}

func (a *agent) removeMapBackup(name string) error {
	file := filepath.Join(a.path(mapBackupDir), name)
	_ = os.Remove(file + ".json")
	return os.Remove(file)
}

// roomSettingsActions turns saved clean_sets rows [suction, water, repeats, order, mode, extra]
// back into the app's two map edits: customeClean (order becomes 0) and then cleanOrder.
func roomSettingsActions(rooms map[string][]int) []map[string]any {
	ids := make([]int, 0, len(rooms))
	for k := range rooms {
		if id, err := strconv.Atoi(k); err == nil && len(rooms[k]) >= 6 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool {
		oi, oj := rooms[strconv.Itoa(ids[i])][3], rooms[strconv.Itoa(ids[j])][3]
		if (oi == 0) != (oj == 0) {
			return oj == 0 // rooms without an order go last
		}
		return oi < oj || oi == oj && ids[i] < ids[j]
	})
	rows, order := [][]int{}, []int{}
	for _, id := range ids {
		r := rooms[strconv.Itoa(id)]
		rows = append(rows, []int{id, r[0], r[1], r[2], r[4], r[5]})
		if r[3] != 0 {
			order = append(order, id)
		}
	}
	out := []map[string]any{{"customeClean": rows}}
	if len(order) > 0 {
		out = append(out, map[string]any{"cleanOrder": order})
	}
	return out
}

// afterRestore waits for 6/11 = 3 and puts the room settings back.
func (a *agent) afterRestore(mapID int, rooms map[string][]int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start, moved := time.Now(), false
	for ctx.Err() == nil {
		time.Sleep(time.Second)
		res, err := a.call(ctx, request{Method: "get_properties", Params: json.RawMessage(`[{"siid":6,"piid":11}]`)})
		raw, _ := json.Marshal(res)
		done := err == nil && strings.Contains(string(raw), `"value":3`)
		moved = moved || err == nil && !done || a.roomSettings(mapID) == nil // the restore clears them
		// 3 may still be left from an earlier restore: trust it once the state moved, or after 30 s.
		if !done || !moved && time.Since(start) < 30*time.Second {
			continue
		}
		for _, edit := range roomSettingsActions(rooms) {
			value, _ := json.Marshal(edit)
			params, _ := json.Marshal(map[string]any{"siid": 6, "aiid": 2, "in": []any{map[string]any{"piid": 4, "value": string(value)}}})
			if _, err := a.call(ctx, request{Method: "action", Params: params}); err != nil {
				log.Printf("map restore: room settings not put back: %v", err)
			}
		}
		return
	}
	log.Printf("map restore: no finished state in 2 minutes; room settings not put back")
}

func copyTree(from, to string) error {
	st, err := os.Stat(from)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return copyFile(from, to)
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(to, 0700); err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTree(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func backupParam(raw json.RawMessage) (string, error) {
	var p struct {
		Name string `json:"name"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || backupNameRe.FindStringSubmatch(p.Name) == nil {
		return "", errors.New("expected {\"name\": a backup name}")
	}
	return p.Name, nil
}

// restoreMapBackup offers the package once on a random loopback URL and asks the firmware to fetch it.
func (a *agent) restoreMapBackup(ctx context.Context, raw json.RawMessage, did string) (any, error) {
	name, err := backupParam(raw)
	if err != nil {
		return nil, err
	}
	m := backupNameRe.FindStringSubmatch(name)
	file := filepath.Join(a.path(mapBackupDir), name)
	if _, err := os.Stat(file); err != nil {
		return nil, errors.New("no such backup")
	}
	a.dev.mu.Lock()
	activity, _ := a.dev.snap.State["activity"].(string)
	a.dev.mu.Unlock()
	if activity != "" && activity != "docked" && activity != "idle" {
		return nil, errors.New("stop the robot before restoring a map")
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	key := hex.EncodeToString(b)
	restoreOffer.Lock()
	restoreOffer.key, restoreOffer.file, restoreOffer.until = key, file, time.Now().Add(restoreURLLife)
	restoreOffer.Unlock()
	mapID, _ := strconv.Atoi(m[1])
	value, _ := json.Marshal(map[string]any{"map_id": mapID, "map_url": fmt.Sprintf("http://127.0.0.1:%d/map-restore/%s", a.cfg.Port, key)})
	var rooms map[string][]int
	if b, err := os.ReadFile(file + ".json"); err == nil {
		_ = json.Unmarshal(b, &rooms)
	}
	result, err := a.miot(ctx, "set_properties", []any{map[string]any{"did": did, "siid": 6, "piid": 10, "value": string(value)}})
	if err == nil && rooms != nil {
		go a.afterRestore(mapID, rooms)
	}
	return result, err
}

// serveRestoreFile answers the firmware's wget; only from the robot itself and only once.
func serveRestoreFile(w http.ResponseWriter, r *http.Request) bool {
	key, ok := strings.CutPrefix(r.URL.Path, "/map-restore/")
	if !ok {
		return false
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	restoreOffer.Lock()
	file := restoreOffer.file
	valid := r.Method == "GET" && net.ParseIP(host).IsLoopback() && restoreOffer.key != "" && key == restoreOffer.key && time.Now().Before(restoreOffer.until)
	if valid {
		restoreOffer.key = ""
	}
	restoreOffer.Unlock()
	if !valid {
		http.Error(w, "not found", 404)
		return true
	}
	w.Header().Set("Content-Type", "application/x-bzip2")
	http.ServeFile(w, r, file)
	return true
}
