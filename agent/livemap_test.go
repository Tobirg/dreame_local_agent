package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testLiveFrame builds a frame of the given kind ('I' or 'P') like the r2505 ones.
func testLiveFrame(t *testing.T, kind byte, meta string) string {
	t.Helper()
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	raw[4] = kind
	return strings.TrimRight(encodeTestFrame(t, append(raw[:36], []byte(meta)...)), "=")
}

func TestLivePose(t *testing.T) {
	a := testAgent(t)
	if p := a.livePose(); p.Live {
		t.Fatal("live without frames")
	}
	now := time.Now().UnixMilli()
	// Start of a run: an I frame with the positions as strings and no path yet.
	writeFile(t, a, liveFrameFiles[1], testLiveFrame(t, 'I', fmt.Sprintf(`{"robot":"[5,455]","charger":"[5,455]","tr":"","mtid":"run1","timestamp_ms":%d}`, now-9000)))
	if p := a.livePose(); !p.Live || !reflect.DeepEqual(p.Robot, []int{5, 455}) || !reflect.DeepEqual(p.Charger, []int{5, 455}) || len(p.Path) != 0 {
		t.Fatalf("%+v", p)
	}
	// Moving: P frames in the IoT log, each with the path since the previous one.
	line := func(ms int64, robot, tr, run string) string {
		frame := testLiveFrame(t, 'P', fmt.Sprintf(`{"robot":%s,"charger":[32767,32767],"curid":"7","tr":%q,"mtid":%q,"timestamp_ms":%d}`, robot, tr, run, ms))
		return `2026-11-22 18:11:54.550888 AVA_IOT[1811]:  [I] <AvaSendSimpleMsg> msgcvt>iot]{"id":1,"method":"properties_changed","params":[{"did":"1","siid":4,"piid":63,"value":41},{"did":"1","siid":6,"piid":1,"value":"` + frame + `"}]}` + "\n"
	}
	// The log rotated during the run: its start is in ava_iot-<date>.log.
	writeFile(t, a, "/data/log/ava_iot-20261122-182530.log", line(now-60*60000, "[0,0]", "l0,0L5,5", "old run")+
		line(now-6000, "[-2877,-1622]", "l-2942,-1623L51,0", "run1"))
	writeFile(t, a, iotLogFile, `unrelated log line "siid":6,"piid":8,"value":"x"`+"\n"+
		line(now-3000, "[-2793,-1627]", "l-2840,-1624W-1687,-653L-10,0", "run1"))
	p := a.livePose()
	if !p.Live || p.MapID != 7 || !reflect.DeepEqual(p.Robot, []int{-2793, -1627}) || !reflect.DeepEqual(p.Charger, []int{5, 455}) {
		t.Fatalf("%+v", p)
	}
	if !reflect.DeepEqual(p.Path, [][]int{{-2942, -1623, 0}, {-2891, -1623, 0}, {-2840, -1624, 0}, {-1687, -653, 1}, {-1697, -653, 1}}) {
		t.Fatal("path of this run only, first point absolute and the rest relative:", p.Path)
	}
	// The next run starts with an I frame that still carries run1's mtid: no old path.
	writeFile(t, a, liveFrameFiles[0], testLiveFrame(t, 'I', fmt.Sprintf(`{"robot":"[0,425]","tr":"","mtid":"run1","timestamp_ms":%d}`, now-1000)))
	if p := a.livePose(); !p.Live || !reflect.DeepEqual(p.Robot, []int{0, 425}) || len(p.Path) != 0 {
		t.Fatalf("%+v", p)
	}
	writeFile(t, a, liveFrameFiles[0], "")
	// After the run nothing new arrives.
	writeFile(t, a, iotLogFile, line(now-2*int64(liveFrameMaxAge/time.Millisecond), "[1,1]", "", "run1"))
	writeFile(t, a, liveFrameFiles[1], "")
	writeFile(t, a, "/data/log/ava_iot-20261122-182530.log", "")
	if a.livePose().Live {
		t.Fatal("stale frames reported as live")
	}
}

func TestParsePathMode(t *testing.T) {
	points, mop := parsePath("S-10,0L5,0", 1)
	if mop != 0 || !reflect.DeepEqual(points, [][]int{{-10, 0, 0}, {-5, 0, 0}}) {
		t.Fatal(points, mop)
	}
	if points, mop = parsePath("l0,0L1,1", 1); mop != 1 || points[1][2] != 1 {
		t.Fatal("l keeps the mode", points)
	}
}

func TestLiveFrameTargets(t *testing.T) {
	f, ok := parseLiveFrame(testLiveFrame(t, 'P', `{"robot":[0,0],"sp":[[-4400,800,1,1,2]],"mtid":"r","timestamp_ms":1}`))
	if !ok || !reflect.DeepEqual(f.spots, [][]int{{-4400, 800}}) {
		t.Fatal(f.spots, ok)
	}
	if f, ok = parseLiveFrame(testLiveFrame(t, 'P', `{"robot":[0,0],"sp":"odd","da2":7,"mtid":"r","timestamp_ms":1}`)); !ok || f.spots != nil || f.zones != nil {
		t.Fatal("odd sp or da2 must not drop the frame", ok)
	}
	// Zone cleaning as the X50 reports it.
	f, _ = parseLiveFrame(testLiveFrame(t, 'P', `{"robot":[0,0],"da2":{"areas":[[-5000,1000,-3800,2200,1,1,2,1]]},"mtid":"r","timestamp_ms":1}`))
	if !reflect.DeepEqual(f.zones, [][]int{{-5000, 1000, -3800, 2200}}) {
		t.Fatal(f.zones)
	}
}

func TestLiveCells(t *testing.T) {
	// Saved map: 2x1 cells of 50 mm from (0, 0), the left one room 1.
	view := mapView{GridSize: 50, Origin: []int{0, 0}, Width: 2, Height: 1, Pixels: []byte{1, 0}}
	frames := []liveFrame{
		// The run's I frame: room 1 (4) on both cells and one more to the right, outside the saved map.
		{full: true, run16: 7, seq: 0, grid: 50, ox: 0, oy: 0, w: 3, h: 1, cells: []byte{4, 4, 0}},
		// An older run's I frame must not be used.
		{full: true, run16: 6, seq: 9, grid: 50, ox: 0, oy: 0, w: 3, h: 1, cells: []byte{0, 0, 0}},
		// P frames add changes: the third cell becomes a wall (252), then the second cell is cleared.
		{run16: 7, seq: 2, grid: 50, ox: 50, oy: 0, w: 1, h: 1, cells: []byte{252}},
		{run16: 7, seq: 1, grid: 50, ox: 100, oy: 0, w: 1, h: 1, cells: []byte{252}},
	}
	// seq 1 (third cell 0+252) comes before seq 2 (second cell 4+252 = 0 mod 256).
	got := liveCells(frames, 7, view)
	if !reflect.DeepEqual(got, [][]int{{100, 0, 2}}) {
		t.Fatal(got)
	}
	if liveCells(frames, 8, view) != nil {
		t.Fatal("no I frame of the run, no cells")
	}
}
