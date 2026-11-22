package main

import (
	"os"
	"reflect"
	"testing"
	"time"
)

func TestLivePose(t *testing.T) {
	a := testAgent(t)
	if p := a.livePose(); p.Live {
		t.Fatal("live without frames")
	}
	raw, err := decodeSavedFrame(syntheticFrame(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:36], []byte(`{"origin":[-100,-200],"robot":[-40,-120],"charger":[5,455],"tr":"M-50,-150L-40,-150l0,30l-10,0"}`)...)
	writeFile(t, a, liveFrameFiles[1], encodeTestFrame(t, raw))
	p := a.livePose()
	if !p.Live || p.MapID != 2 || !reflect.DeepEqual(p.Robot, []int{-40, -120}) || !reflect.DeepEqual(p.Charger, []int{5, 455}) {
		t.Fatalf("%+v", p)
	}
	if !reflect.DeepEqual(p.Path, [][]int{{-50, -150}, {-40, -150}, {-40, -120}, {-50, -120}}) {
		t.Fatal(p.Path)
	}
	old := time.Now().Add(-2 * liveFrameMaxAge)
	_ = os.Chtimes(a.path(liveFrameFiles[1]), old, old)
	if a.livePose().Live {
		t.Fatal("stale frame reported as live")
	}
}
