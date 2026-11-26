package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A caller cancelled while it waited for the gate (a patrol the owner stopped) sends nothing.
func TestCallCancelledWhileWaitingSendsNothing(t *testing.T) {
	a, calls := monitorAgent(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.call(ctx, request{Method: "action", Params: json.RawMessage(`{"siid":3,"aiid":1,"in":[]}`)}); err == nil {
		t.Fatal("cancelled call went through")
	}
	if got := calls(); len(got) != 1 || got[0] != "" {
		t.Fatal(got)
	}
}

// A drive step that lands while the idle end waits for the gate keeps the session open.
func TestIdleEndYieldsToDriveStep(t *testing.T) {
	a, calls := monitorAgent(t, 2)
	stop := make(chan struct{})
	monitor.Lock()
	monitor.open, monitor.stop, monitor.last = true, stop, time.Now().Add(-time.Minute)
	monitor.Unlock()
	a.gate <- struct{}{} // a command is running
	ended := make(chan bool)
	go func() { ended <- a.endIdleMonitor(stop) }()
	time.Sleep(100 * time.Millisecond)
	monitor.Lock()
	monitor.last = time.Now() // that command was a drive step
	monitor.Unlock()
	<-a.gate
	if <-ended {
		t.Fatal("session ended right after a drive step")
	}
	for _, c := range calls() {
		if strings.Contains(c, `\"operation\":\"end\"`) {
			t.Fatal("end sent: " + c)
		}
	}
	monitor.Lock()
	monitor.last = time.Now().Add(-time.Minute)
	monitor.Unlock()
	if !a.endIdleMonitor(stop) || monitor.open {
		t.Fatal("idle session not ended")
	}
	if got := calls(); !strings.Contains(got[len(got)-1], `\"operation\":\"end\"`) {
		t.Fatal(got)
	}
}

// Saving the video code again lets the agent try it again, even when it is the same code.
func TestVideoCodeSaveClearsRefusal(t *testing.T) {
	a, _ := monitorAgent(t, 2)
	a.configPath = a.path("config.json")
	if err := atomicPrivate(a.configPath, []byte(`{"token":"x"}`)); err != nil {
		t.Fatal(err)
	}
	monitor.refused = a.cfg.VideoCodeSHA256
	r := httptest.NewRequest("POST", "/api/v1/system/video_code", strings.NewReader(`{"code":"1234"}`))
	a.videoCodeRoute(httptest.NewRecorder(), r, func(code int, v any) {
		if code != 200 {
			t.Fatal(code, v)
		}
	})
	if monitor.refused != "" {
		t.Fatal("refusal kept")
	}
}

func TestWpaSSIDDecodesCyrillic(t *testing.T) {
	// wpa_cli status for the network "Дом"
	if got := wpaSSID(`\xd0\x94\xd0\xbe\xd0\xbc`); got != "Дом" {
		t.Fatal(got)
	}
	if got := wpaSSID(`Home "2"`); got != `Home "2"` {
		t.Fatal(got)
	}
}
