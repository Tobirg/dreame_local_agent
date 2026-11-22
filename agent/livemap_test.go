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
	if !reflect.DeepEqual(p.Path, [][]int{{-2942, -1623}, {-2891, -1623}, {-2840, -1624}, {-1687, -653}, {-1697, -653}}) {
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
