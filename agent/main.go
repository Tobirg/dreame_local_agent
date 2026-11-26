// Copyright 2026 Dreame Local contributors. MIT license.
// Independent on-device agent using the firmware's avacmd and local map file.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Set at build time for test or release builds: -ldflags "-X main.version=..."
var version = "0.30.4"

//go:embed ui/index.html ui/style.css ui/app.js ui/settings.js ui/pages.js ui/actions.js ui/voice.js ui/phrases.js ui/monitor.js
var webAssets embed.FS

const maxMap = 8 << 20

type config struct {
	Bind          string `json:"bind"`
	Port          int    `json:"port"`
	Token         string `json:"token"`
	ExpectedModel string `json:"expected_model"`
	ExpectedDID   string `json:"expected_did"`
	ExpectedMAC   string `json:"expected_mac"`
	AllowControl  bool   `json:"allow_control"`
	// Commands and setting changes are refused on firmware that matches no checked profile unless this is set.
	AllowUnverifiedFirmware bool       `json:"allow_unverified_firmware,omitempty"`
	Tokens                  []apiToken `json:"tokens,omitempty"`
	// Origins (scheme://host[:port]) that may show the panel in a frame, e.g. a Home Assistant dashboard card.
	EmbedOrigins []string `json:"embed_origins,omitempty"`
	// sha256 (hex) of the owner's video access code; opens the video monitoring session (monitor.go).
	VideoCodeSHA256 string `json:"video_code_sha256,omitempty"`
	// While the agent holds a video monitoring session, refuse new outbound connections to the internet.
	VideoCloudBlock bool `json:"video_cloud_block,omitempty"`
	// Room label size on the panel map in screen pixels; 0 keeps the automatic 10–14 px.
	LabelSize int `json:"label_size,omitempty"`
	// Key the module tokens are derived from; set to the old password the first time the password changes.
	ModuleSecret string `json:"module_secret,omitempty"`
}
type request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type agent struct {
	memoMu sync.Mutex
	memos  map[string]*commandMemo

	rebootQueued  atomic.Bool
	rebootCommand string   // Only overridden by tests.
	iptables      []string // argv prefix for the video cloud block (monitor.go); tests record instead

	portal       portalState
	network      networkJob
	dev          device
	modules      moduleHost
	adb          adbControl
	adbWatch     adbWatch
	configPath   string // absolute; the update helper is started with it
	cfg          config
	tokensMu     sync.RWMutex  // guards cfg.Tokens, cfg.Token and cfg.ModuleSecret
	authFailures chan struct{} // one failed sign-in at a time (password.go)
	authDelay    time.Duration
	root         string // Only set by tests; runtime always uses the actual root filesystem.
	avacmd       string
	gate         chan struct{}
	next         atomic.Int64
}

func (a *agent) path(p string) string { return filepath.Join(a.root, p) }
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return b, nil
}
func (a *agent) identity() (map[string]string, error) {
	info := map[string]string{}
	for _, p := range []string{"/data/config/dmio/device.conf", "/data/config/miio/device.conf"} {
		b, err := readLimited(a.path(p), 65536)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("cannot read device identity")
		}
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok || (k != "model" && k != "did" && k != "mac") {
				continue
			}
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if k == "mac" {
				v = strings.ToLower(v)
			}
			if prev, ok := info[k]; ok && prev != v {
				return nil, fmt.Errorf("conflicting identity field: %s", k)
			}
			info[k] = v
		}
	}
	for _, k := range []string{"model", "did", "mac"} {
		if info[k] == "" {
			return nil, fmt.Errorf("missing identity field: %s", k)
		}
	}
	for k, v := range map[string]string{"model": a.cfg.ExpectedModel, "did": a.cfg.ExpectedDID, "mac": a.cfg.ExpectedMAC} {
		if v != "" && !strings.EqualFold(info[k], v) {
			return nil, fmt.Errorf("robot identity mismatch: %s", k)
		}
	}
	info["fw_ver"] = "unknown"
	if b, err := readLimited(a.path("/etc/os-release"), 65536); err == nil {
		var release map[string]any
		if json.Unmarshal(b, &release) == nil {
			if v, ok := release["fw_arm_ver"].(string); ok {
				info["fw_ver"] = v
			}
		}
	}
	return info, nil
}
func decodeParams(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
func validID(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	i, err := n.Int64()
	return err == nil && i > 0 && i <= 65535
}
func validate(method string, params any) ([]map[string]any, bool, error) {
	var items []map[string]any
	switch method {
	case "action":
		item, ok := params.(map[string]any)
		if !ok {
			return nil, false, errors.New("action requires an object")
		}
		items = append(items, item)
	case "get_properties", "set_properties":
		list, ok := params.([]any)
		if !ok || len(list) == 0 || len(list) > 128 {
			return nil, false, errors.New("expected 1..128 properties")
		}
		for _, v := range list {
			item, ok := v.(map[string]any)
			if !ok {
				return nil, false, errors.New("invalid property object")
			}
			items = append(items, item)
		}
	default:
		return nil, false, errors.New("unsupported RPC method")
	}
	for _, item := range items {
		id := "piid"
		if method == "action" {
			id = "aiid"
		}
		if !validID(item["siid"]) || !validID(item[id]) {
			return nil, false, errors.New("invalid service/property/action ID")
		}
		if method == "set_properties" {
			if _, ok := item["value"]; !ok {
				return nil, false, errors.New("missing property value")
			}
		}
		if method == "action" {
			if v, ok := item["in"]; ok {
				if _, ok := v.([]any); !ok {
					return nil, false, errors.New("action in must be an array")
				}
			}
		}
	}
	// Even a map request can publish work; no action is a blanket read.
	return items, method == "get_properties", nil
}
func parseReply(output []byte, id int64) (json.RawMessage, error) {
	for pos, c := range output {
		if c != '{' {
			continue
		}
		var reply struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.NewDecoder(bytes.NewReader(output[pos:])).Decode(&reply) != nil || reply.ID != id {
			continue
		}
		if len(reply.Error) > 0 && string(reply.Error) != "null" {
			return nil, errors.New("robot returned an RPC error")
		}
		if len(reply.Result) > 0 {
			return reply.Result, nil
		}
	}
	return nil, errors.New("no matching robot response; command outcome unknown; not retried")
}

// limitedBuffer prevents malformed or excessive firmware output exhausting RAM.
type limitedBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (16 << 20) - b.buf.Len()
	if remaining <= 0 {
		b.overflow = true
		return n, nil
	}
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, err := b.buf.Write(p)
	return n, err
}
func (a *agent) call(ctx context.Context, req request) (any, error) {
	select {
	case a.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-a.gate }()
	// select picks at random when both are ready: a caller cancelled while it waited (a patrol the
	// owner stopped) must not send its step after the command that stopped it.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := a.identity()
	if err != nil {
		return nil, err
	}
	params, err := decodeParams(req.Params)
	if err != nil {
		return nil, errors.New("invalid RPC parameters")
	}
	switch req.Method {
	case "miIO.info":
		return info, nil
	case "local.settings":
		return a.readSettings(ctx, info["did"])
	case "local.setting":
		if err := a.robotWriteAllowed(info); err != nil {
			return nil, err
		}
		return a.changeSetting(ctx, req.Params, info["did"])
	case "local.command":
		if err := a.robotWriteAllowed(info); err != nil {
			return nil, err
		}
		return a.runCommand(ctx, req.Params, info)
	case "local.reboot":
		if !a.controlAllowed() {
			return nil, errors.New("reboot requires pinned identity and allow_control")
		}
		if string(req.Params) != "null" && len(req.Params) != 0 {
			return nil, errors.New("local.reboot takes no parameters")
		}
		return a.queueReboot()
	case "local.thresholds", "local.restrictions", "local.room_settings", "local.room_order", "local.room_types", "local.room_floors", "local.map_use", "local.carpets", "local.carpet_types", "local.carpet_clean", "local.furniture":
		if err := a.robotWriteAllowed(info); err != nil {
			return nil, err
		}
		build := map[string]func(json.RawMessage) (map[string]any, error){
			"local.thresholds": a.thresholdPatch, "local.restrictions": a.restrictionsPatch, "local.room_settings": a.roomSettingsPatch, "local.room_order": a.roomOrderPatch, "local.room_types": a.roomTypesPatch, "local.room_floors": a.roomFloorsPatch, "local.map_use": a.useMapEdit, "local.carpets": a.carpetsPatch, "local.carpet_types": a.carpetTypesPatch, "local.carpet_clean": a.carpetCleanPatch, "local.furniture": a.furniturePatch,
		}[req.Method]
		patch, err := build(req.Params)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(patch)
		if err != nil {
			return nil, err
		}
		input := map[string]any{"did": info["did"], "siid": 6, "aiid": 2, "in": []any{map[string]any{"piid": 4, "value": string(value)}}}
		return a.miot(ctx, "action", input)
	case "local.messages_status":
		a.dev.mu.Lock()
		defer a.dev.mu.Unlock()
		return a.readMessages(), nil
	case "local.patrol_schedules_status":
		patrolSchedulesMu.Lock()
		defer patrolSchedulesMu.Unlock()
		return a.readPatrolSchedules(), nil
	case "local.patrol_schedule", "local.patrol_schedule_delete":
		if err := a.robotWriteAllowed(info); err != nil {
			return nil, err
		}
		if req.Method == "local.patrol_schedule_delete" {
			return a.deletePatrolSchedule(req.Params)
		}
		return a.setPatrolSchedule(req.Params, info["did"])
	case "local.messages_clear":
		a.dev.mu.Lock()
		defer a.dev.mu.Unlock()
		return map[string]any{"cleared": true}, a.writeMessages([]message{})
	case "local.map_backup_status":
		if params != nil {
			return nil, errors.New("local.map_backup_status takes no parameters")
		}
		restore, err := a.miot(ctx, "get_properties", []any{map[string]any{"did": info["did"], "siid": 6, "piid": 11}})
		if err != nil {
			restore = nil // the list is still useful
		}
		return map[string]any{"backups": a.mapBackups(), "restore": restore, "maps": a.savedMaps()}, nil
	case "local.map_backup", "local.map_restore", "local.map_backup_delete":
		if err := a.robotWriteAllowed(info); err != nil {
			return nil, err
		}
		switch req.Method {
		case "local.map_backup":
			if params != nil {
				return nil, errors.New("local.map_backup takes no parameters")
			}
			return a.createMapBackup()
		case "local.map_restore":
			return a.restoreMapBackup(ctx, req.Params, info["did"])
		}
		name, err := backupParam(req.Params)
		if err != nil {
			return nil, err
		}
		return map[string]any{"deleted": name}, a.removeMapBackup(name)
	case "local.wifi_status":
		if params != nil {
			return nil, errors.New("local.wifi_status takes no parameters")
		}
		return a.wifiStatus(), nil
	case "local.wifi_setup":
		return a.setupWiFi(ctx, req.Params)
	case "local.live_pose":
		if params != nil {
			return nil, errors.New("local.live_pose takes no parameters")
		}
		return a.livePose(), nil
	case "local.map_view":
		if params != nil {
			return nil, errors.New("local.map_view takes no parameters")
		}
		return a.mapView()
	case "local.maps":
		if params != nil {
			return nil, errors.New("local.maps takes no parameters")
		}
		b, err := readLimited(a.path("/data/log/map_info.bin"), maxMap)
		if os.IsNotExist(err) {
			return map[string]any{"mapstr": []any{}, "curr_id": nil}, nil
		}
		if err != nil {
			return nil, errors.New("cannot read saved map file")
		}
		var maps map[string]json.RawMessage
		if json.Unmarshal(b, &maps) != nil {
			return nil, errors.New("saved map file is not valid JSON; retry on next poll")
		}
		var entries []json.RawMessage
		if raw, ok := maps["mapstr"]; !ok || json.Unmarshal(raw, &entries) != nil || string(raw) == "null" {
			return nil, errors.New("invalid saved map file schema")
		}
		return json.RawMessage(b), nil
	}
	items, readOnly, err := validate(req.Method, params)
	if err != nil {
		return nil, err
	}
	if !readOnly {
		if err := a.robotWriteAllowed(info); err != nil {
			return nil, err
		}
	}
	for _, item := range items {
		item["did"] = info["did"]
	}
	return a.miot(ctx, req.Method, params)
}
func (a *agent) miot(ctx context.Context, method string, params any) (any, error) {
	id := a.next.Add(1)
	payload, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	out, err := a.runAvacmd(ctx, "msg_cvt"+string(payload))
	if err != nil {
		return nil, err
	}
	result, err := parseReply(out, id)
	if err != nil {
		return nil, err
	}
	if isMapEditAction(method, params) {
		if err := validateMapReply(result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (a *agent) runAvacmd(ctx context.Context, args ...string) ([]byte, error) {
	task, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(task, a.avacmd, args...) // Direct argv, no shell.
	cmd.WaitDelay = time.Second
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if task.Err() != nil {
			return nil, errors.New("avacmd timed out/cancelled; command outcome unknown; not retried")
		}
		return nil, errors.New("avacmd failed; command outcome unknown; not retried")
	}
	if out.overflow {
		return nil, errors.New("robot response exceeds size limit")
	}
	return out.buf.Bytes(), nil
}

var originRe = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)

// frameAncestors is the CSP source list for pages that may frame the panel: none unless embed_origins names some.
func (a *agent) frameAncestors() string {
	ok := []string{}
	a.tokensMu.RLock()
	defer a.tokensMu.RUnlock()
	for _, o := range a.cfg.EmbedOrigins {
		if originRe.MatchString(o) {
			ok = append(ok, o)
		}
	}
	if len(ok) == 0 {
		return "'none'"
	}
	return strings.Join(ok, " ")
}

func (a *agent) handler(w http.ResponseWriter, r *http.Request) {

	// The panel compares this between responses to notice an agent update and reload its pages.
	w.Header().Set("X-Agent-Version", version)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; frame-ancestors "+a.frameAncestors()+"; form-action 'none'; base-uri 'none'")
	if r.Method == "GET" {
		asset := ""
		contentType := ""
		switch r.URL.Path {
		case "/":
			asset = "ui/index.html"
			contentType = "text/html; charset=utf-8"
		case "/style.css":
			asset = "ui/style.css"
			contentType = "text/css; charset=utf-8"
		case "/settings.js", "/pages.js", "/actions.js", "/voice.js", "/phrases.js", "/monitor.js":
			asset = "ui" + r.URL.Path
			contentType = "text/javascript; charset=utf-8"
		case "/app.js":
			asset = "ui/app.js"
			contentType = "text/javascript; charset=utf-8"
		}
		if asset == "" && a.servePanel(w, r.URL.Path) {
			return
		}
		if asset != "" {
			data, err := webAssets.ReadFile(asset)
			if err != nil {
				http.Error(w, "asset unavailable", 500)
				return
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(data)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	send := func(code int, v any) {
		if code >= 500 {
			b, _ := json.Marshal(v)
			log.Printf("%s %s: %d %s", r.Method, r.URL.Path, code, b) // shown in the panel's log
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	if serveRestoreFile(w, r) { // the firmware's wget has no token; see mapbackup.go
		return
	}
	scope, module := a.scopeOf(r.Header.Get("Authorization")), ""
	if scope == "" {
		if module = a.moduleForToken(r.Header.Get("Authorization")); module == "" {
			a.refuseUnauthorized(send)
			return
		}
		scope = "module"
	}
	if scope == "module" && r.URL.Path == "/api/v1/command" {
		if m, err := a.installedModule(module); err == nil && m.Control {
			scope = "control" // declared in the manifest and shown to the owner at install
		}
	}
	if scope == "module" && a.crossModuleRead(module, r) {
		scope = "admin" // only this one route: another module's declared read action
	}
	if scope != "admin" && !limitedRoute(scope, r) {
		send(403, map[string]string{"error": scope + " token cannot use this endpoint"})
		return
	}
	if module != "" && r.URL.Path == "/api/v1/firmware_message" {
		// A module's own token may also send the firmware messages its manifest declares.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			send(400, map[string]string{"error": "invalid firmware message"})
			return
		}
		result, err := a.firmwareMessage(r.Context(), module, body)
		if err != nil {
			send(502, rpcErrorResponse(err))
			return
		}
		send(200, map[string]any{"result": result})
		return
	}
	if a.moduleRoutes(w, r, send) || a.systemRoutes(w, r, send) || a.firmwareRoutes(w, r, send) || a.tokenRoutes(w, r, send) || a.voiceRoutes(w, r, send) || a.logRoutes(r, send) {
		return
	}
	switch r.URL.Path {
	case "/api/v1/map.svg":
		if r.Method != "GET" {
			send(405, map[string]string{"error": "GET required"})
			return
		}
		img, err := a.mapSVG(mapOptsFromQuery(r.URL.Query()))
		if err != nil {
			send(404, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(img)
		return
	case "/api/v1/map":
		if r.Method != "GET" {
			send(405, map[string]string{"error": "GET required"})
			return
		}
		card, err := a.mapCard()
		if err != nil {
			send(404, map[string]string{"error": err.Error()})
			return
		}
		send(200, map[string]any{"result": card})
		return
	case "/api/v1/settings":
		if r.Method != "GET" {
			send(405, map[string]string{"error": "GET required"})
			return
		}
		result, err := a.call(r.Context(), request{Method: "local.settings", Params: json.RawMessage("null")})
		if err != nil {
			send(502, rpcErrorResponse(err))
			return
		}
		send(200, map[string]any{"result": result})
		return
	case "/api/v1/history":
		if r.Method != "GET" {
			send(405, map[string]string{"error": "GET required"})
			return
		}
		send(200, map[string]any{"result": a.readHistory()})
		return
	case "/api/v1/history/photo":
		name := r.URL.Query().Get("name")
		if r.Method != "GET" || !runPhotoName.MatchString(name) {
			send(400, map[string]string{"error": "GET with a photo name required"})
			return
		}
		img, err := readLimited(filepath.Join(a.path(runPhotoDir), name), 4<<20)
		if err != nil {
			send(404, map[string]string{"error": "photo not found"})
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(img)
		return
	case "/api/v1/pets/photos":
		if r.Method != "GET" {
			send(405, map[string]string{"error": "GET required"})
			return
		}
		send(200, map[string]any{"result": a.petPhotos()})
		return
	case "/api/v1/pets/photo":
		name := r.URL.Query().Get("name")
		if r.Method != "GET" || !petPhotoName.MatchString(name) {
			send(400, map[string]string{"error": "GET with a photo name required"})
			return
		}
		img, err := a.petPhotoFile(name)
		if err != nil {
			send(404, map[string]string{"error": "photo not found"})
			return
		}
		w.Header().Set("Content-Type", petContentType(name))
		w.Header().Set("Cache-Control", "private, no-store")
		_, _ = w.Write(img)
		return
	case "/api/v1/pets/delete":
		var req struct {
			Name string `json:"name"`
		}
		if r.Method != "POST" || json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
			send(400, map[string]string{"error": "POST with a photo name required"})
			return
		}
		if err := a.deletePetPhoto(req.Name); err != nil {
			switch {
			case errors.Is(err, errPetPhotoNotFound):
				send(404, map[string]string{"error": err.Error()})
			case !petPhotoName.MatchString(req.Name) && !runPhotoName.MatchString(req.Name):
				send(400, map[string]string{"error": err.Error()})
			default:
				log.Printf("pet photo delete: %v", err)
				send(500, map[string]string{"error": "cannot delete the photo"})
			}
			return
		}
		send(200, map[string]any{"result": map[string]string{"deleted": req.Name}})
		return
	case "/api/v1/camera/stream":
		a.cameraStream(w, r, send)
		return
	case "/api/v1/events":
		if r.Method != "GET" {
			send(405, map[string]string{"error": "GET required"})
			return
		}
		a.serveEvents(w, r, send)
		return
	case "/api/v1/state", "/api/v1/capabilities":
		if r.Method != "GET" {
			send(405, map[string]string{"error": "GET required"})
			return
		}
		var result any = a.snapshot() // served from cache: never waits for the robot
		if r.URL.Path == "/api/v1/capabilities" {
			caps, err := a.capabilities()
			if err != nil {
				send(502, rpcErrorResponse(err))
				return
			}
			result = caps
		}
		send(200, map[string]any{"result": result})
		return
	case "/rpc", "/api/v1/command":
	default:
		send(404, map[string]string{"error": "not found"})
		return
	}
	if r.Method != "POST" {
		send(405, map[string]string{"error": "POST required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	var req request
	if r.URL.Path == "/api/v1/command" {
		// Same gate, control policy and request-ID memo as /rpc, under one method name.
		raw, err := io.ReadAll(r.Body)
		if err != nil || !json.Valid(raw) {
			send(400, map[string]string{"error": "invalid command"})
			return
		}
		req = request{Method: "local.command", Params: raw}
	} else {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			send(400, map[string]string{"error": "invalid RPC envelope"})
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			send(400, map[string]string{"error": "trailing JSON data"})
			return
		}
	}
	if r.URL.Path == "/rpc" && (scope == "read" || scope == "control") && !requestIsRead(req) {
		send(403, map[string]string{"error": scope + " token can only read through /rpc"})
		return
	}
	if id := r.Header.Get("X-Dreame-Request-ID"); id != "" && !requestIsRead(req) {
		status, body := a.memoCall(r.Context(), req, id)
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	result, err := a.call(r.Context(), req)
	if err != nil {
		send(502, rpcErrorResponse(err))
		return
	}
	send(200, map[string]any{"result": result})
}

// startDetached creates a new session before the child executes. Shell nohup alone
// leaves a race with the firmware ADB launcher closing its process group.
func startDetached(executable string, args []string, logPath, pidPath string) (int, error) {
	flags := os.O_WRONLY | os.O_APPEND | os.O_CREATE
	if st, err := os.Stat(logPath); err == nil && st.Size() > 256<<10 {
		flags |= os.O_TRUNC // /tmp is in memory
	}
	output, err := os.OpenFile(logPath, flags, 0600)
	if err != nil {
		return 0, err
	}
	defer output.Close()
	input, err := os.Open(os.DevNull)
	if err != nil {
		return 0, err
	}
	defer input.Close()
	cmd := exec.Command(executable, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)+"\n"), 0600); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return 0, err
	}
	if err := cmd.Process.Release(); err != nil {
		return 0, err
	}
	return pid, nil
}

// keepAgent runs the agent and starts it again when it dies, so a crash does not leave the
// robot without local control until the next boot. A signal (stop.sh, updates) stops both.
func keepAgent(agent func() *exec.Cmd, stop <-chan os.Signal, delay time.Duration) {
	first := delay
	for {
		cmd := agent()
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		started := time.Now()
		if err := cmd.Start(); err != nil {
			log.Print("cannot start agent: ", err)
		} else {
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case <-stop:
				_ = cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-exited:
				case <-time.After(10 * time.Second): // stopAgent gives up on us after 15 s
					_ = cmd.Process.Kill()
					<-exited
				}
				return
			case err := <-exited:
				log.Printf("agent exited (%v); restarting", err)
			}
		}
		if time.Since(started) > 10*time.Minute {
			delay = first
		}
		select {
		case <-stop:
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, time.Minute)
	}
}

func main() {
	cfgPath := flag.String("config", "/data/dreame-local/config.json", "configuration file")
	daemon := flag.Bool("daemon", false, "start in a detached session")
	showVersion := flag.Bool("version", false, "print agent version")
	applyUpdate := flag.String("apply-update", "", "internal: replace the installed agent with this binary")
	updateID := flag.String("update-id", "", "internal: id of the update request")
	keep := flag.Bool("keep", false, "internal: run the agent and restart it when it dies")
	flag.Parse()
	if *showVersion {
		fmt.Println("dreame-local-agent " + version)
		return
	}
	data, err := readLimited(*cfgPath, 65536)
	if err != nil {
		log.Fatal("cannot read configuration")
	}
	cfg := config{Bind: "0.0.0.0", Port: 8765}
	if json.Unmarshal(data, &cfg) != nil {
		log.Fatal("invalid configuration")
	}
	if validPassword(cfg.Token) != nil || strings.HasPrefix(cfg.Token, "REPLACE") || cfg.Port < 1 || cfg.Port > 65535 {
		log.Fatal("invalid password (token) or port")
	}
	if *applyUpdate != "" {
		runUpdateHelper(cfg, *applyUpdate, *updateID)
		return
	}
	if *keep {
		executable, err := os.Executable()
		if err != nil {
			log.Fatal("cannot locate agent executable")
		}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		keepAgent(func() *exec.Cmd { return exec.Command(executable, "--config", *cfgPath) }, signals, 5*time.Second)
		return
	}
	path, err := exec.LookPath("avacmd")
	if err != nil {
		log.Fatal("avacmd not found in PATH")
	}
	a := &agent{cfg: cfg, avacmd: path, gate: make(chan struct{}, 1), iptables: []string{"iptables"}, authFailures: make(chan struct{}, 1), authDelay: time.Second}
	a.cloudBlock(false) // a rule left by a crash while a session was open
	if abs, err := filepath.Abs(*cfgPath); err == nil {
		a.configPath = abs
	}
	a.next.Store(time.Now().Unix())
	if _, err := a.identity(); err != nil {
		log.Fatal(err)
	}
	if *daemon {
		executable, err := os.Executable()
		if err != nil {
			log.Fatal("cannot locate agent executable")
		}
		absoluteConfig, err := filepath.Abs(*cfgPath)
		if err != nil {
			log.Fatal("cannot resolve configuration path")
		}
		pid, err := startDetached(executable, []string{"--keep", "--config", absoluteConfig}, agentLogFile, "/tmp/dreame-local-agent.pid")
		if err != nil {
			log.Fatal("cannot start detached agent: ", err)
		}
		fmt.Printf("Agent detached: pid=%d\n", pid)
		return
	}
	server := &http.Server{Addr: cfg.Bind + ":" + strconv.Itoa(cfg.Port), Handler: http.HandlerFunc(a.handler), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16384}
	portalContext, stopPortal := context.WithCancel(context.Background())
	defer stopPortal()
	portalDone := make(chan struct{})
	go func() { defer close(portalDone); a.runPortal(portalContext) }()
	a.dev.poke = make(chan struct{}, 1)
	a.modules.slots = make(chan struct{}, 4)
	a.modules.ctx = portalContext
	a.startServices()
	a.applyFirmwareBlock()
	go a.runPoller(portalContext, 5*time.Second)
	go a.runPatrolSchedules(portalContext)
	go a.runPetPhotos(portalContext)
	go a.runADBWatch(portalContext)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		<-signals
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(ctx) != nil {
			_ = server.Close()
		}
		close(done)
	}()
	log.Print("dreame-local-agent " + version + " ready")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-done
	a.stopServices()
	stopPortal()
	<-portalDone
}
