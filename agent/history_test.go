package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestTrackRun(t *testing.T) {
	a := testAgent(t)
	state := func(activity, status string, minutes, area int) map[string]any {
		return map[string]any{"activity": activity, "status_name": status, "error_name": "bin_full",
			"stats": map[string]any{"minutes": json.Number(itoa(minutes)), "area": json.Number(itoa(area))}}
	}
	poll := func(s map[string]any, fresh bool) {
		a.dev.mu.Lock()
		a.trackRun(s, fresh)
		a.dev.mu.Unlock()
	}
	poll(state("docked", "charging", 300, 260), true)
	if a.dev.run != nil {
		t.Fatal("run started while docked")
	}
	poll(state("cleaning", "room_cleaning", 300, 260), false)
	poll(state("paused", "paused", 300, 260), false)
	poll(state("returning", "returning", 300, 260), false)
	if len(a.readHistory()) != 0 {
		t.Fatal("run recorded before it ended")
	}
	poll(state("docked", "charging", 300, 260), false) // ended; totals not re-read yet
	if !a.dev.detailsAt.IsZero() || len(a.readHistory()) != 0 {
		t.Fatal("end must ask for fresh totals before recording")
	}
	poll(state("docked", "charging", 312, 271), true)
	runs := a.readHistory()
	if len(runs) != 1 || runs[0].Mode != "room_cleaning" || runs[0].Result != "docked" || runs[0].Error != "" ||
		*runs[0].Minutes != 12 || *runs[0].Area != 11 || runs[0].End == nil || a.dev.run != nil {
		t.Fatalf("%+v", runs)
	}
	// A run that ends in an error keeps the error name; newest first.
	poll(state("cleaning", "cleaning", 312, 271), false)
	poll(state("error", "error", 312, 271), false)
	poll(state("error", "error", 315, 273), true)
	if runs = a.readHistory(); len(runs) != 2 || runs[0].Result != "error" || runs[0].Error != "bin_full" || *runs[0].Area != 2 {
		t.Fatalf("%+v", runs[0])
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestRunObstaclePhotos(t *testing.T) {
	a := testAgent(t)
	start := time.Now().Add(-10 * time.Minute).UTC()
	end := start.Add(9 * time.Minute)
	if err := a.appendHistory(cleanRun{Start: start, End: &end, Mode: "zone_cleaning"}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, a, "/data/record/ai_image/1.jpg", "jpeg1")
	// Shoe with a photo, an old-style entry without one, and a path outside the firmware's folder.
	writeFile(t, a, fdsLogDir+"/1795437478", testLiveFrame(t, 'I', fmt.Sprintf(`{"curid":"2","timestamp_ms":%d,"ai_obstacle":[`+
		`["-4110","-610","133","0.96","1795437306","/data/record/ai_image/1.jpg"],["1","2","137","0.9","0","0"],["3","4","138","0.8","0","/data/dreame-local/config.json"]]}`,
		end.Add(-time.Second).UnixMilli())))
	a.keepRunObstacles(start, end)
	runs := a.readHistory()
	if !reflect.DeepEqual(runs[0].Photos, []string{fmt.Sprintf("%d-0.jpg", start.Unix()), "", ""}) || len(runs[0].Obstacles) != 3 {
		t.Fatalf("%+v", runs[0])
	}
	if b, _ := os.ReadFile(filepath.Join(a.path(runPhotoDir), runs[0].Photos[0])); string(b) != "jpeg1" {
		t.Fatal("photo not copied", string(b))
	}
	// A list from before the run is not attached to it.
	later := end.Add(time.Hour)
	laterEnd := later.Add(time.Minute)
	_ = a.appendHistory(cleanRun{Start: later, End: &laterEnd})
	a.keepRunObstacles(later, laterEnd)
	if runs = a.readHistory(); runs[0].Obstacles != nil || len(runs[1].Photos) != 3 {
		t.Fatalf("%+v", runs)
	}
	if !runPhotoName.MatchString(runs[1].Photos[0]) || runPhotoName.MatchString("../config.json") {
		t.Fatal("photo name check")
	}
}
