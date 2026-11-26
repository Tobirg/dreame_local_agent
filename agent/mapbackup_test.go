package main

import (
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMapBackupAndRestoreFile(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("no tar")
	}
	a := testAgent(t)
	if _, err := a.createMapBackup(); err == nil {
		t.Fatal("backup made without a saved map")
	}
	maps, _ := json.Marshal(map[string]any{"curr_id": 2, "mapstr": []any{map[string]any{"id": 0, "map": syntheticFrame(t, 2, 500)}}})
	writeFile(t, a, "/data/log/map_info.bin", string(maps))
	view, _ := a.mapView()
	id := itoa(view.MapID)
	writeFile(t, a, "/data/map/"+id+"/app_map.bin", "map")
	writeFile(t, a, "/data/ri/"+id+".dat2", "dat")
	writeFile(t, a, "/data/DivideAI/ai_result/"+id+"/ai_rooms_large.txt", "ai")
	writeFile(t, a, "/data/DivideMap/"+id+"/doors_info_large.json", "{}")
	b, err := a.createMapBackup()
	if err != nil || b.MapID != view.MapID || b.Size == 0 {
		t.Fatal(b, err)
	}
	list, err := exec.Command("tar", "-tjf", a.path(mapBackupDir+"/"+b.Name)).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{id + "/app_map.bin", id + ".dat2", "ai/" + id + "/ai_rooms_large.txt", "divide/" + id + "/doors_info_large.json"} {
		if !strings.Contains(string(list), want) {
			t.Fatalf("%s missing in %s", want, list)
		}
	}
	// The file is served once, to loopback, under the offered key only.
	restoreOffer.key, restoreOffer.file, restoreOffer.until = "k1", a.path(mapBackupDir+"/"+b.Name), time.Now().Add(time.Minute)
	get := func(path, from string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = from
		w := httptest.NewRecorder()
		if !serveRestoreFile(w, r) {
			return 0
		}
		return w.Code
	}
	if get("/map-restore/k1", "192.168.2.5:4000") != 404 || get("/map-restore/k2", "127.0.0.1:4000") != 404 {
		t.Fatal("served to the wrong caller")
	}
	if get("/map-restore/k1", "127.0.0.1:4000") != 200 || get("/map-restore/k1", "127.0.0.1:4000") != 404 {
		t.Fatal("not served exactly once")
	}
	if get("/api/v1/state", "127.0.0.1:4000") != 0 {
		t.Fatal("handled another path")
	}
	// Settings as on the X50: order 4,3,2,1 for rooms 1,3,4,5; room 6 has no order.
	edits := roomSettingsActions(map[string][]int{"1": {1, 3, 1, 4, 2, 546}, "3": {1, 3, 1, 3, 2, 546}, "4": {1, 3, 1, 2, 2, 546}, "5": {1, 3, 1, 1, 2, 546}, "6": {0, 1, 2, 0, 0, 0}})
	got, _ := json.Marshal(edits)
	if string(got) != `[{"customeClean":[[5,1,3,1,2,546],[4,1,3,1,2,546],[3,1,3,1,2,546],[1,1,3,1,2,546],[6,0,1,2,0,0]]},{"cleanOrder":[5,4,3,1]}]` {
		t.Fatal(string(got))
	}
	if _, err := backupParam(json.RawMessage(`{"name":"../x"}`)); err == nil {
		t.Fatal("bad name accepted")
	}
}

func TestUseMap(t *testing.T) {
	a := testAgent(t)
	writeFile(t, a, "/data/ri/config.json2", `{"order":[2,5],"version":1,"curr_id":2}`)
	if m := a.savedMaps(); m["current"] != 2 {
		t.Fatal(m)
	}
	if _, err := a.useMapEdit(json.RawMessage(`{"map_id":7}`)); err == nil {
		t.Fatal("unknown map accepted")
	}
	got, err := a.useMapEdit(json.RawMessage(`{"map_id":5}`))
	if b, _ := json.Marshal(got); err != nil || string(b) != `{"mapid":5,"sm":{}}` {
		t.Fatal(string(b), err)
	}
}
