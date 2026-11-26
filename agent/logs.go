package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	agentLogFile = "/tmp/dreame-local-agent.log"
	logTail      = 32 << 10
)

type logSource struct {
	Source string `json:"source"`
	Text   string `json:"text"`
	Size   int64  `json:"size"`
}

// tailFile returns the last n bytes of a file, starting at a whole line.
func tailFile(path string, n int64) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	start := max(st.Size()-n, 0)
	b, err := io.ReadAll(io.NewSectionReader(f, start, st.Size()-start))
	if err != nil {
		return "", 0, err
	}
	if i := strings.IndexByte(string(b), '\n'); start > 0 && i >= 0 {
		b = b[i+1:]
	}
	return string(b), st.Size(), nil
}

// logs: the agent's own log, the last update's log and each module's service log.
func (a *agent) logs() []logSource {
	files := [][2]string{{"agent", agentLogFile}, {"update", updateLogFile}}
	entries, _ := os.ReadDir(a.path(moduleDataBase))
	for _, e := range entries {
		if e.IsDir() && moduleNameRe.MatchString(e.Name()) {
			files = append(files, [2]string{e.Name(), filepath.Join(moduleDataBase, e.Name(), "service.log")})
		}
	}
	out := []logSource{}
	for _, f := range files {
		if text, size, err := tailFile(a.path(f[1]), logTail); err == nil {
			out = append(out, logSource{Source: f[0], Text: text, Size: size})
		}
	}
	return out
}

// logRoutes: GET /api/v1/logs. Main token only (limitedRoute): logs name hosts and devices.
func (a *agent) logRoutes(r *http.Request, send func(int, any)) bool {
	if r.URL.Path != "/api/v1/logs" {
		return false
	}
	if r.Method != "GET" {
		send(405, map[string]string{"error": "GET required"})
		return true
	}
	send(200, map[string]any{"result": a.logs()})
	return true
}
