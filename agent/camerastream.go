package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Live camera stream for the monitoring screen: the camera module's camera-live service keeps the
// newest frame in /tmp/dreame-camera/live.jpg while someone touches "want" (module handler action
// "live"). Polling that action costs a handler run and base64 per frame, about 2-3 fps; this route
// sends each new frame as it lands (multipart/x-mixed-replace, up to the sensor's 15 fps) and keeps
// "want" fresh. Requires the camera module; main and control tokens only (limitedRoute).

const (
	cameraLiveDir   = "/tmp/dreame-camera"
	cameraStreamFor = 15 * time.Minute // a forgotten tab does not keep the camera on for ever
	maxCameraFrame  = 1 << 20
)

func (a *agent) cameraStream(w http.ResponseWriter, r *http.Request, send func(int, any)) {
	if r.Method != "GET" {
		send(405, map[string]string{"error": "GET required"})
		return
	}
	if _, err := a.installedModule("camera"); err != nil {
		send(404, map[string]string{"error": "camera module is not installed"})
		return
	}
	dir := a.path(cameraLiveDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		send(500, map[string]string{"error": "cannot reach the camera module"})
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.WriteHeader(200)
	part := func(kind string, data []byte) bool {
		if _, err := fmt.Fprintf(w, "--frame\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n", kind, len(data)); err != nil {
			return false
		}
		if _, err := w.Write(append(data, '\r', '\n')); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	want, frame, status := filepath.Join(dir, "want"), filepath.Join(dir, "live.jpg"), filepath.Join(dir, "status.json")
	var last time.Time
	var touched, said time.Time
	end := time.Now().Add(cameraStreamFor)
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for time.Now().Before(end) {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
		if time.Since(touched) > time.Second {
			now := time.Now()
			if f, err := os.OpenFile(want, os.O_CREATE|os.O_WRONLY, 0600); err == nil {
				f.Close()
				_ = os.Chtimes(want, now, now)
			}
			touched = now
		}
		st, err := os.Stat(frame)
		if err == nil && st.ModTime().After(last) && time.Since(st.ModTime()) < 3*time.Second {
			b, err := readLimited(frame, maxCameraFrame)
			if err != nil || len(b) == 0 {
				continue
			}
			last = st.ModTime()
			if !part("image/jpeg", b) {
				return
			}
			continue
		}
		// No new frame: say what the service is doing every 2 s (starting, robot asleep, error).
		if time.Since(said) > 2*time.Second && time.Since(last) > 2*time.Second {
			said = time.Now()
			b, err := readLimited(status, 4096)
			if err != nil || len(b) == 0 {
				b = []byte(`{"state":"starting","reason":""}`)
			}
			if !part("application/json", b) {
				return
			}
		}
	}
}
