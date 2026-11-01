// Copyright 2026 Dreame Local contributors. MIT license.
// Independent on-device agent using the firmware's avacmd and local map file.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"debug/elf"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const version = "0.5.0"

//go:embed ui/index.html ui/style.css ui/app.js ui/settings.js
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
}
type request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type agent struct {
	memoMu sync.Mutex
	memos  map[string]*commandMemo

	rebootQueued  atomic.Bool
	rebootCommand string // Only overridden by tests.

	portal  portalState
	network networkJob
	cfg     config
	root    string // Only set by tests; runtime always uses the actual root filesystem.
	avacmd  string
	gate    chan struct{}
	next    atomic.Int64
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
	readOnly := method == "get_properties"
	if method == "action" {
		readOnly = items[0]["siid"] == json.Number("6") && items[0]["aiid"] == json.Number("1")
	}
	return items, readOnly, nil
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
	case "local.setting":
		if !a.controlAllowed() {
			return nil, errors.New("setting changes require pinned identity and allow_control")
		}
		return a.changeSetting(ctx, req.Params, info["did"])
	case "local.reboot":
		if !a.controlAllowed() {
			return nil, errors.New("reboot requires pinned identity and allow_control")
		}
		if string(req.Params) != "null" && len(req.Params) != 0 {
			return nil, errors.New("local.reboot takes no parameters")
		}
		return a.queueReboot()
	case "local.thresholds":
		if !a.controlAllowed() {
			return nil, errors.New("map editing requires pinned identity and allow_control")
		}
		patch, err := a.thresholdPatch(req.Params)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(patch)
		if err != nil {
			return nil, err
		}
		input := map[string]any{"did": info["did"], "siid": 6, "aiid": 2, "in": []any{map[string]any{"piid": 4, "value": string(value)}}}
		return a.miot(ctx, "action", input)
	case "local.wifi_status":
		if params != nil {
			return nil, errors.New("local.wifi_status takes no parameters")
		}
		return a.wifiStatus(), nil
	case "local.wifi_setup":
		return a.setupWiFi(ctx, req.Params)
	case "local.region_guard_status":
		if params != nil {
			return nil, errors.New("local.region_guard_status takes no parameters")
		}
		return a.guardStatus(), nil
	case "local.region_guard":
		if !a.controlAllowed() {
			return nil, errors.New("control requires pinned identity and allow_control")
		}
		options, ok := params.(map[string]any)
		if !ok || len(options) != 1 {
			return nil, errors.New("expected enabled boolean")
		}
		enabled, ok := options["enabled"].(bool)
		if !ok {
			return nil, errors.New("expected enabled boolean")
		}
		path := a.path("/data/dreame-local/region-guard.enabled")
		if enabled {
			if err := atomicPrivate(path, []byte("1\n")); err != nil {
				return nil, errors.New("cannot save regional guard mode")
			}
		} else {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return nil, errors.New("cannot disable regional guard")
			}
		}
		return a.guardStatus(), nil
	case "local.region_lock":
		if !a.cfg.AllowControl || a.cfg.ExpectedModel == "" || a.cfg.ExpectedDID == "" || a.cfg.ExpectedMAC == "" {
			return nil, errors.New("control requires allow_control and pinned model, did, mac")
		}
		options, ok := params.(map[string]any)
		if !ok || len(options) != 1 || (options["code"] != json.Number("200") && options["code"] != json.Number("401")) {
			return nil, errors.New("local.region_lock requires only code: 200 or 401")
		}
		payload, err := json.Marshal(map[string]any{"type": "msgCvt", "cmd": "device_lock", "code": options["code"]})
		if err != nil {
			return nil, err
		}
		if _, err := a.runAvacmd(ctx, "msg_cvt", string(payload)); err != nil {
			return nil, err
		}
		// This firmware message has no matching MIOT RPC reply. Do not claim confirmation.
		return map[string]any{"dispatched": true, "confirmed": false, "requested_code": options["code"]}, nil
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
	if !readOnly && (!a.cfg.AllowControl || a.cfg.ExpectedModel == "" || a.cfg.ExpectedDID == "" || a.cfg.ExpectedMAC == "") {
		return nil, errors.New("control requires allow_control and pinned model, did, mac")
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
	return parseReply(out, id)
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

func (a *agent) handler(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'; form-action 'none'; base-uri 'none'")
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
		case "/settings.js":
			asset = "ui/settings.js"
			contentType = "text/javascript; charset=utf-8"
		case "/app.js":
			asset = "ui/app.js"
			contentType = "text/javascript; charset=utf-8"
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
	send := func(code int, v any) { w.WriteHeader(code); _ = json.NewEncoder(w).Encode(v) }
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.cfg.Token)) != 1 {
		send(401, map[string]string{"error": "unauthorized"})
		return
	}
	if r.URL.Path != "/rpc" {
		send(404, map[string]string{"error": "not found"})
		return
	}
	if r.Method != "POST" {
		send(405, map[string]string{"error": "POST required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req request
	if err := decoder.Decode(&req); err != nil {
		send(400, map[string]string{"error": "invalid RPC envelope"})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		send(400, map[string]string{"error": "trailing JSON data"})
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
		send(502, map[string]string{"error": err.Error()})
		return
	}
	send(200, map[string]any{"result": result})
}

// startDetached creates a new session before the child executes. Shell nohup alone
// leaves a race with the firmware ADB launcher closing its process group.
func startDetached(executable string, args []string, logPath, pidPath string) (int, error) {
	output, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
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

func main() {
	cfgPath := flag.String("config", "/data/dreame-local/config.json", "configuration file")
	checkGuard := flag.Bool("check-guard", false, "check firmware regional setter compatibility")
	daemon := flag.Bool("daemon", false, "start in a detached session")
	showVersion := flag.Bool("version", false, "print agent version")
	flag.Parse()
	if *showVersion {
		fmt.Println("dreame-local-agent " + version)
		return
	}
	if *checkGuard {
		file, err := elf.Open("/ava/lib/node_msg_cvt.so")
		if err != nil {
			log.Fatal("cannot inspect firmware regional setter")
		}
		defer file.Close()
		symbols, err := file.DynamicSymbols()
		if err != nil {
			log.Fatal("cannot inspect firmware symbols")
		}
		for _, symbol := range symbols {
			if symbol.Name == "_ZN13avanodemsgcvt9MsgConfig13SetDeviceLockEi" && symbol.Section != elf.SHN_UNDEF {
				fmt.Println("Regional guard: firmware setter found")
				return
			}
		}
		log.Fatal("unsupported firmware regional setter")
	}
	data, err := readLimited(*cfgPath, 65536)
	if err != nil {
		log.Fatal("cannot read configuration")
	}
	cfg := config{Bind: "0.0.0.0", Port: 8765}
	if json.Unmarshal(data, &cfg) != nil {
		log.Fatal("invalid configuration")
	}
	if len(cfg.Token) < 32 || strings.HasPrefix(cfg.Token, "REPLACE") || cfg.Port < 1 || cfg.Port > 65535 {
		log.Fatal("invalid API token or port")
	}
	path, err := exec.LookPath("avacmd")
	if err != nil {
		log.Fatal("avacmd not found in PATH")
	}
	a := &agent{cfg: cfg, avacmd: path, gate: make(chan struct{}, 1)}
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
		pid, err := startDetached(executable, []string{"--config", absoluteConfig}, "/tmp/dreame-local-agent.log", "/tmp/dreame-local-agent.pid")
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
	stopPortal()
	<-portalDone
}
