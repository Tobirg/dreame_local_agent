// Copyright 2026 Dreame Local contributors. MIT license.
// Self-update from the web UI. The running agent only stages and checks the new
// binary; the swap is done by a detached helper (the current binary started with
// --apply-update), because the agent it replaces must be stopped. The helper
// keeps the old binary as dreame-local-agent.previous, starts the new one and
// rolls back when it does not answer with the expected version in time.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	agentBase      = "/data/dreame-local"
	agentPIDFile   = "/tmp/dreame-local-agent.pid"
	updatePIDFile  = "/tmp/dreame-local-update.pid"
	updateLogFile  = "/tmp/dreame-local-update.log"
	maxAgentBinary = 32 << 20
)

var versionRe = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,32}$`)

type updateResult struct {
	ID       string `json:"id"`
	Status   string `json:"status"` // applying, updated, rolled_back, failed
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Error    string `json:"error,omitempty"`
	Finished string `json:"finished,omitempty"`
}

func (a *agent) agentFile(name string) string { return filepath.Join(a.path(agentBase), name) }

// binaryVersion runs "<path> --version" and expects "dreame-local-agent <version>".
func binaryVersion(ctx context.Context, path string) (string, error) {
	task, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(task, path, "--version")
	var out limitedBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", errors.New("file does not run as an agent on this robot")
	}
	v, ok := strings.CutPrefix(strings.TrimSpace(out.buf.String()), "dreame-local-agent ")
	if !ok || !versionRe.MatchString(v) {
		return "", errors.New("file is not a Dreame Local agent")
	}
	return v, nil
}

func (a *agent) stageUpdate(ctx context.Context, data []byte) (map[string]any, error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("file is not a Linux executable")
	}
	want := map[string]elf.Machine{"arm64": elf.EM_AARCH64, "amd64": elf.EM_X86_64}[runtime.GOARCH]
	if f.Machine != want || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return nil, fmt.Errorf("executable is for %v, this robot needs %v", f.Machine, want)
	}
	staged := a.agentFile("agent.update")
	tmp := staged + ".tmp"
	if err := os.WriteFile(tmp, data, 0700); err != nil {
		return nil, errors.New("cannot save the new agent")
	}
	if err := os.Rename(tmp, staged); err != nil {
		_ = os.Remove(tmp)
		return nil, errors.New("cannot save the new agent")
	}
	v, err := binaryVersion(ctx, staged)
	if err != nil {
		_ = os.Remove(staged)
		return nil, err
	}
	sum := sha256.Sum256(data)
	return map[string]any{"version": v, "current": version, "sha256": hex.EncodeToString(sum[:]), "size": len(data)}, nil
}

func pidAlive(path string) bool {
	b, err := readLimited(path, 64)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 1 && syscall.Kill(pid, 0) == nil
}

func (a *agent) updateStatus(ctx context.Context) map[string]any {
	status := map[string]any{"current": version, "staged": nil, "previous": nil, "last": nil, "applying": pidAlive(a.path(updatePIDFile))}
	if v, err := binaryVersion(ctx, a.agentFile("agent.update")); err == nil {
		status["staged"] = v
	}
	if v, err := binaryVersion(ctx, a.agentFile("dreame-local-agent.previous")); err == nil {
		status["previous"] = v
	}
	var last updateResult
	if b, err := readLimited(a.agentFile("update-result.json"), 65536); err == nil && json.Unmarshal(b, &last) == nil {
		status["last"] = last
	}
	return status
}

// startUpdate launches the detached helper; source is "staged" or "previous" (rollback).
func (a *agent) startUpdate(source string) (any, error) {
	files := map[string]string{"staged": "agent.update", "previous": "dreame-local-agent.previous"}
	name, ok := files[source]
	if !ok {
		return nil, errors.New("source must be staged or previous")
	}
	path := a.agentFile(name)
	if _, err := os.Stat(path); err != nil {
		return nil, errors.New("no " + source + " agent to install")
	}
	if pidAlive(a.path(updatePIDFile)) {
		return nil, errors.New("an update is already running")
	}
	if a.configPath == "" {
		return nil, errors.New("agent was started without a configuration path")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, errors.New("cannot locate agent executable")
	}
	id := randomHex(8)
	writeResult(a.agentFile("update-result.json"), updateResult{ID: id, Status: "applying"})
	if _, err := startDetached(executable, []string{"--config", a.configPath, "--apply-update", path, "--update-id", id}, updateLogFile, updatePIDFile); err != nil {
		writeResult(a.agentFile("update-result.json"), updateResult{ID: id, Status: "failed", Error: "cannot start update helper"})
		return nil, errors.New("cannot start update helper")
	}
	return map[string]any{"applying": true, "id": id}, nil
}

func writeResult(path string, r updateResult) {
	if r.Status != "applying" {
		r.Finished = time.Now().UTC().Format(time.RFC3339)
	}
	b, _ := json.Marshal(r)
	_ = atomicPrivate(path, b)
}

// updater performs the swap; its operations are fields so tests can replace them.
type updater struct {
	base, source, id string
	versionOf        func(path string) (string, error)
	stop, start      func() error
	healthy          func(version string) error
}

func (u updater) run() updateResult {
	current := filepath.Join(u.base, "dreame-local-agent")
	previous := current + ".previous"
	swap := current + ".swap"
	result := updateResult{ID: u.id, Status: "failed"}
	to, err := u.versionOf(u.source)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	from, _ := u.versionOf(current)
	result.From, result.To = from, to
	if err := u.stop(); err != nil {
		result.Error = "cannot stop the running agent: " + err.Error()
		_ = u.start()
		return result
	}
	// current -> previous, source -> current; works for the staged file and for a rollback from previous.
	if err := os.Rename(current, swap); err != nil {
		result.Error = "cannot move the running agent aside"
		_ = u.start()
		return result
	}
	if err := os.Rename(u.source, current); err != nil {
		_ = os.Rename(swap, current)
		result.Error = "cannot put the new agent in place"
		_ = u.start()
		return result
	}
	// A failed update must not cost the existing rollback target: park it until the new agent is healthy.
	older := previous + ".old"
	_ = os.Remove(older)
	hadOlder := u.source != previous && os.Rename(previous, older) == nil
	_ = os.Rename(swap, previous)
	err = u.start()
	if err == nil {
		err = u.healthy(to)
	}
	if err == nil {
		_ = os.Remove(older)
		result.Status = "updated"
		return result
	}
	result.Error = "new agent did not come up: " + err.Error()
	_ = u.stop()
	_ = os.Rename(current, current+".failed")
	if rerr := os.Rename(previous, current); rerr != nil {
		result.Error += "; cannot restore the previous agent"
		return result
	}
	if hadOlder {
		_ = os.Rename(older, previous)
	}
	if rerr := u.start(); rerr == nil {
		rerr = u.healthy(from)
		if rerr == nil {
			result.Status = "rolled_back"
			return result
		}
	}
	result.Error += "; previous agent did not come back either"
	return result
}

// runUpdateHelper is the --apply-update mode of the binary.
func runUpdateHelper(cfg config, source, id string) {
	base := agentBase
	host := "127.0.0.1"
	if cfg.Bind != "" && cfg.Bind != "0.0.0.0" && cfg.Bind != "::" {
		host = cfg.Bind
	}
	client := &http.Client{Timeout: 5 * time.Second}
	u := updater{
		base: base, source: source, id: id,
		versionOf: func(path string) (string, error) { return binaryVersion(context.Background(), path) },
		stop:      stopAgent,
		start: func() error {
			task, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return exec.CommandContext(task, filepath.Join(base, "start.sh")).Run()
		},
		healthy: func(want string) error {
			deadline := time.Now().Add(45 * time.Second)
			last := "no answer"
			for time.Now().Before(deadline) {
				time.Sleep(time.Second)
				req, _ := http.NewRequest("GET", "http://"+host+":"+strconv.Itoa(cfg.Port)+"/api/v1/capabilities", nil)
				req.Header.Set("Authorization", "Bearer "+cfg.Token)
				res, err := client.Do(req)
				if err != nil {
					continue
				}
				var body struct {
					Result struct {
						Agent struct {
							Version string `json:"version"`
						} `json:"agent"`
					} `json:"result"`
					Error string `json:"error"`
				}
				_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body)
				res.Body.Close()
				if res.StatusCode == 200 && body.Result.Agent.Version == want {
					return nil
				}
				last = fmt.Sprintf("HTTP %d %s", res.StatusCode, body.Error)
			}
			return errors.New(strings.TrimSpace(last))
		},
	}
	writeResult(filepath.Join(base, "update-result.json"), u.run())
}

// stopAgent stops the agent recorded in its pid file, never this helper.
func stopAgent() error {
	b, err := readLimited(agentPIDFile, 64)
	if err != nil {
		return nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 || pid == os.Getpid() {
		return nil
	}
	if syscall.Kill(pid, syscall.SIGTERM) != nil {
		return nil
	}
	for i := 0; i < 15; i++ {
		time.Sleep(time.Second)
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(time.Second)
	if syscall.Kill(pid, 0) == nil {
		return errors.New("agent does not stop")
	}
	return nil
}

// extendDeadlines gives a large upload time to arrive and its answer time to leave: the server's
// write deadline runs from the request headers, so without the second one a slow upload is
// processed but its reply is cut and the panel reports a failure.
func extendDeadlines(w http.ResponseWriter, upload time.Duration) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(upload))
	_ = rc.SetWriteDeadline(time.Now().Add(upload + time.Minute))
}

// systemRoutes handles /api/v1/system/update*; false when the path is not one of them.
func (a *agent) systemRoutes(w http.ResponseWriter, r *http.Request, send func(int, any)) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/v1/system/update") {
		return false
	}
	reply := func(v any, err error) {
		if err != nil {
			send(502, rpcErrorResponse(err))
			return
		}
		send(200, map[string]any{"result": v})
	}
	if p == "/api/v1/system/update" {
		if r.Method != "GET" {
			send(405, map[string]string{"error": "GET required"})
			return true
		}
		send(200, map[string]any{"result": a.updateStatus(r.Context())})
		return true
	}
	if r.Method != "POST" || (p != "/api/v1/system/update/stage" && p != "/api/v1/system/update/apply") {
		send(404, map[string]string{"error": "not found"})
		return true
	}
	if !a.controlAllowed() {
		send(403, map[string]string{"error": "updating the agent requires pinned identity and allow_control"})
		return true
	}
	if p == "/api/v1/system/update/stage" {
		extendDeadlines(w, 5*time.Minute)
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAgentBinary))
		if err != nil {
			send(400, map[string]string{"error": "file too large or upload interrupted"})
			return true
		}
		reply(a.stageUpdate(r.Context(), data))
		return true
	}
	var req struct {
		Source string `json:"source"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		send(400, map[string]string{"error": "invalid request"})
		return true
	}
	reply(a.startUpdate(req.Source))
	return true
}
