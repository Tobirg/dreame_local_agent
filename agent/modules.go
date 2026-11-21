// Copyright 2026 Dreame Local contributors. MIT license.
// Installable modules: a module is a .tar.gz package unpacked into its own
// directory under /data/dreame-local/modules. The core never links module code;
// it runs the module's handler executable per request, serves its panel script,
// sources its boot-env.sh from the firmware boot hook, and relays only the
// firmware messages the module's manifest declares.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	modulesBase    = "/data/dreame-local/modules"
	maxPackage     = 32 << 20
	maxPackageFile = 256
	maxHandlerOut  = 1 << 20
)

var (
	moduleNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	actionRe     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	fieldRe      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
	stageRe      = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

type manifest struct {
	Name             string   `json:"name"`
	Version          string   `json:"version"`
	Title            string   `json:"title"`
	Description      string   `json:"description,omitempty"`
	Handler          string   `json:"handler,omitempty"`           // executable, run as: handler <action>
	Panel            string   `json:"panel,omitempty"`             // ES module for the web UI
	ReadActions      []string `json:"read_actions,omitempty"`      // actions allowed without allow_control
	FirmwareMessages []string `json:"firmware_messages,omitempty"` // msg_cvt cmd values the module may send
}

type moduleHost struct {
	mu     sync.Mutex
	tokens map[string]string // per-process random token -> module name
	slots  chan struct{}     // bounds concurrent handler processes
}

func (a *agent) modulesDir() string { return a.path(modulesBase) }

func validRelative(p string) bool {
	return p != "" && !path.IsAbs(p) && path.Clean(p) == p && p != "." && !strings.HasPrefix(p, "../") && p != ".."
}

func (m *manifest) validate(dir string) error {
	if !moduleNameRe.MatchString(m.Name) || m.Version == "" || len(m.Version) > 32 || m.Title == "" || len(m.Title) > 80 || len(m.Description) > 500 {
		return errors.New("manifest needs name (a-z0-9_-), version and title")
	}
	for _, f := range []string{m.Handler, m.Panel} {
		if f == "" {
			continue
		}
		if !validRelative(f) {
			return errors.New("manifest file paths must be relative to the package")
		}
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("manifest file %s is missing", f)
		}
	}
	for _, act := range m.ReadActions {
		if !actionRe.MatchString(act) {
			return errors.New("invalid read action name")
		}
	}
	for _, cmd := range m.FirmwareMessages {
		if !fieldRe.MatchString(cmd) {
			return errors.New("invalid firmware message name")
		}
	}
	return nil
}

func readManifest(dir string) (*manifest, error) {
	b, err := readLimited(filepath.Join(dir, "manifest.json"), 65536)
	if err != nil {
		return nil, errors.New("package has no manifest.json")
	}
	var m manifest
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil {
		return nil, errors.New("manifest.json is invalid")
	}
	return &m, m.validate(dir)
}

// unpack extracts regular files and directories only; links, devices and paths escaping dir are refused.
func unpack(data []byte, dir string) ([]string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("package is not a .tar.gz archive")
	}
	tr := tar.NewReader(io.LimitReader(gz, 4*maxPackage))
	var files []string
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("package archive is corrupt")
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		if !validRelative(name) {
			return nil, fmt.Errorf("unsafe path in package: %s", h.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0700); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			total += h.Size
			if len(files) >= maxPackageFile || total > 4*maxPackage {
				return nil, errors.New("package is too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return nil, err
			}
			mode := os.FileMode(0600)
			if h.Mode&0100 != 0 {
				mode = 0700
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return nil, fmt.Errorf("duplicate or unwritable file: %s", name)
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return nil, errors.New("cannot write package file")
			}
			files = append(files, name)
		default:
			return nil, fmt.Errorf("package may contain only files and directories: %s", h.Name)
		}
	}
	sort.Strings(files)
	return files, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// stageModule unpacks an upload for review; nothing runs until install.
func (a *agent) stageModule(data []byte) (map[string]any, error) {
	staging := filepath.Join(a.modulesDir(), ".staging")
	_ = os.RemoveAll(staging) // one pending review at a time
	id := randomHex(8)
	dir := filepath.Join(staging, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("cannot create staging directory")
	}
	files, err := unpack(data, dir)
	if err == nil {
		var m *manifest
		if m, err = readManifest(dir); err == nil {
			sum := sha256.Sum256(data)
			result := map[string]any{"id": id, "sha256": hex.EncodeToString(sum[:]), "size": len(data), "manifest": m, "files": files,
				"boot_hook": contains(files, "boot-env.sh"), "installed_version": nil}
			if old, err := readManifest(filepath.Join(a.modulesDir(), m.Name)); err == nil {
				result["installed_version"] = old.Version
			}
			return result, nil
		}
	}
	_ = os.RemoveAll(staging)
	return nil, err
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// installModule runs the package's check, swaps directories, then runs install;
// a failed install restores the previous version.
func (a *agent) installModule(ctx context.Context, id string) (any, error) {
	if !stageRe.MatchString(id) {
		return nil, errors.New("invalid staged package id")
	}
	staging := filepath.Join(a.modulesDir(), ".staging")
	dir := filepath.Join(staging, id)
	m, err := readManifest(dir)
	if err != nil {
		return nil, errors.New("staged package not found; upload it again")
	}
	if m.Handler != "" {
		if _, err := a.runHandler(ctx, dir, m, "check", nil); err != nil {
			return nil, fmt.Errorf("module check failed: %w", err)
		}
	}
	final := filepath.Join(a.modulesDir(), m.Name)
	backup := filepath.Join(a.modulesDir(), ".previous-"+m.Name)
	_ = os.RemoveAll(backup)
	hadOld := os.Rename(final, backup) == nil
	if err := os.Rename(dir, final); err != nil {
		if hadOld {
			_ = os.Rename(backup, final)
		}
		return nil, errors.New("cannot install module files")
	}
	_ = os.RemoveAll(staging)
	if m.Handler != "" {
		if _, err := a.runHandler(ctx, final, m, "install", nil); err != nil {
			_ = os.RemoveAll(final)
			if hadOld {
				_ = os.Rename(backup, final)
			}
			return nil, fmt.Errorf("module install failed, previous state restored: %w", err)
		}
	}
	_ = os.RemoveAll(backup)
	return map[string]any{"installed": m.Name, "version": m.Version}, nil
}

func (a *agent) removeModule(ctx context.Context, name string) (any, error) {
	m, err := a.installedModule(name)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(a.modulesDir(), name)
	var warning string
	if m.Handler != "" {
		if _, err := a.runHandler(ctx, dir, m, "uninstall", nil); err != nil {
			warning = "uninstall step failed: " + err.Error()
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, errors.New("cannot remove module files")
	}
	return map[string]any{"removed": name, "warning": warning}, nil
}

func (a *agent) installedModule(name string) (*manifest, error) {
	if !moduleNameRe.MatchString(name) {
		return nil, errors.New("invalid module name")
	}
	m, err := readManifest(filepath.Join(a.modulesDir(), name))
	if err != nil || m.Name != name {
		return nil, errors.New("module is not installed")
	}
	return m, nil
}

func (a *agent) listModules() []map[string]any {
	entries, _ := os.ReadDir(a.modulesDir())
	list := []map[string]any{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(a.modulesDir(), e.Name())
		m, err := readManifest(dir)
		if err != nil || m.Name != e.Name() {
			list = append(list, map[string]any{"name": e.Name(), "error": "broken module: manifest missing or invalid"})
			continue
		}
		_, bootErr := os.Stat(filepath.Join(dir, "boot-env.sh"))
		list = append(list, map[string]any{"name": m.Name, "version": m.Version, "title": m.Title, "description": m.Description,
			"panel": m.Panel != "", "boot_hook": bootErr == nil, "firmware_messages": m.FirmwareMessages})
	}
	return list
}

func (a *agent) moduleToken(name string) string {
	a.modules.mu.Lock()
	defer a.modules.mu.Unlock()
	if a.modules.tokens == nil {
		a.modules.tokens = map[string]string{}
	}
	for token, owner := range a.modules.tokens {
		if owner == name {
			return token
		}
	}
	token := randomHex(24)
	a.modules.tokens[token] = name
	return token
}

func (a *agent) moduleForToken(header string) string {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return ""
	}
	a.modules.mu.Lock()
	defer a.modules.mu.Unlock()
	for t, name := range a.modules.tokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			return name
		}
	}
	return ""
}

// runHandler executes "handler <action>" with the JSON params on stdin and scalar
// params as P_<key> variables (convenient for shell handlers). Output must be JSON.
func (a *agent) runHandler(ctx context.Context, dir string, m *manifest, action string, params map[string]any) (json.RawMessage, error) {
	if a.modules.slots == nil {
		return nil, errors.New("module host not started")
	}
	select {
	case a.modules.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-a.modules.slots }()
	input, _ := json.Marshal(params)
	if params == nil {
		input = []byte("{}")
	}
	task, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(task, filepath.Join(dir, filepath.FromSlash(m.Handler)), action)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	cmd.Env = []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin:/ava/bin", "HOME=" + dir, "DREAME_MODULE_DIR=" + dir,
		"DREAME_API=http://127.0.0.1:" + strconv.Itoa(a.cfg.Port), "DREAME_MODULE_TOKEN=" + a.moduleToken(m.Name),
	}
	for k, v := range params {
		if !fieldRe.MatchString(k) {
			continue
		}
		switch v.(type) {
		case string, bool, json.Number:
			cmd.Env = append(cmd.Env, fmt.Sprintf("P_%s=%v", k, v))
		}
	}
	cmd.Stdin = bytes.NewReader(input)
	var out, errOut limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()
	if task.Err() != nil {
		return nil, errors.New("module handler timed out; outcome unknown; not retried")
	}
	body := bytes.TrimSpace(out.buf.Bytes())
	if out.overflow || len(body) > maxHandlerOut {
		return nil, errors.New("module handler output is too large")
	}
	if runErr != nil {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
			return nil, errors.New(failure.Error)
		}
		msg := strings.TrimSpace(errOut.buf.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg == "" {
			msg = runErr.Error()
		}
		return nil, errors.New("module handler failed: " + msg)
	}
	if len(body) == 0 {
		body = []byte("null")
	}
	if !json.Valid(body) {
		return nil, errors.New("module handler returned invalid JSON")
	}
	return json.RawMessage(body), nil
}

func (a *agent) moduleAction(ctx context.Context, name, action string, raw []byte) (any, error) {
	m, err := a.installedModule(name)
	if err != nil {
		return nil, err
	}
	if m.Handler == "" || !actionRe.MatchString(action) {
		return nil, errors.New("module has no such action")
	}
	if !contains(m.ReadActions, action) && !a.controlAllowed() {
		return nil, errors.New("module action requires pinned identity and allow_control")
	}
	var params map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&params) != nil {
			return nil, errors.New("module action parameters must be a JSON object")
		}
	}
	return a.runHandler(ctx, filepath.Join(a.modulesDir(), name), m, action, params)
}

// firmwareMessage sends "avacmd msg_cvt" only for cmd values the calling module declared.
// The firmware sends no matching reply, so success means dispatched, never confirmed.
func (a *agent) firmwareMessage(ctx context.Context, module string, raw []byte) (any, error) {
	if !a.controlAllowed() {
		return nil, errors.New("firmware messages require pinned identity and allow_control")
	}
	m, err := a.installedModule(module)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&fields) != nil || len(fields) == 0 || len(fields) > 8 {
		return nil, errors.New("expected a JSON object with cmd and at most 7 scalar fields")
	}
	cmd, _ := fields["cmd"].(string)
	if !contains(m.FirmwareMessages, cmd) {
		return nil, errors.New("module did not declare this firmware message")
	}
	for k, v := range fields {
		switch v.(type) {
		case string, bool, json.Number:
		default:
			return nil, errors.New("firmware message fields must be scalars")
		}
		if !fieldRe.MatchString(k) || k == "type" {
			return nil, errors.New("invalid firmware message field")
		}
	}
	fields["type"] = "msgCvt"
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	select {
	case a.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-a.gate }()
	if _, err := a.identity(); err != nil {
		return nil, err
	}
	if _, err := a.runAvacmd(ctx, "msg_cvt", string(payload)); err != nil {
		return nil, err
	}
	return map[string]any{"dispatched": true, "confirmed": false, "cmd": cmd}, nil
}

// servePanel returns a module's panel script without auth, like the core UI scripts.
func (a *agent) servePanel(w http.ResponseWriter, urlPath string) bool {
	rest, ok := strings.CutPrefix(urlPath, "/m/")
	name, file, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 || file != "panel.js" {
		return false
	}
	m, err := a.installedModule(name)
	if err != nil || m.Panel == "" {
		http.Error(w, "module panel not found", 404)
		return true
	}
	data, err := readLimited(filepath.Join(a.modulesDir(), name, filepath.FromSlash(m.Panel)), 1<<20)
	if err != nil {
		http.Error(w, "module panel unavailable", 500)
		return true
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
	return true
}

// moduleRoutes handles /api/v1/modules* and /api/v1/m/<name>/<action>; false when the path is not a module route.
func (a *agent) moduleRoutes(w http.ResponseWriter, r *http.Request, send func(int, any)) bool {
	p := r.URL.Path
	reply := func(v any, err error) {
		if err != nil {
			send(502, rpcErrorResponse(err))
			return
		}
		send(200, map[string]any{"result": v})
	}
	readJSON := func(limit int64) ([]byte, bool) {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			send(400, map[string]string{"error": "request body too large or unreadable"})
			return nil, false
		}
		return b, true
	}
	switch {
	case p == "/api/v1/modules":
		if r.Method != "GET" {
			send(405, map[string]string{"error": "GET required"})
			return true
		}
		send(200, map[string]any{"result": a.listModules()})
	case p == "/api/v1/modules/stage", p == "/api/v1/modules/install", p == "/api/v1/modules/remove":
		if r.Method != "POST" {
			send(405, map[string]string{"error": "POST required"})
			return true
		}
		if !a.controlAllowed() {
			send(403, map[string]string{"error": "installing modules requires pinned identity and allow_control"})
			return true
		}
		if p == "/api/v1/modules/stage" {
			// Large packages over Wi-Fi outlast the server-wide read timeout.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute))
			data, ok := readJSON(maxPackage)
			if ok {
				reply(a.stageModule(data))
			}
			return true
		}
		body, ok := readJSON(4096)
		if !ok {
			return true
		}
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(body, &req) != nil {
			send(400, map[string]string{"error": "invalid request"})
			return true
		}
		if p == "/api/v1/modules/install" {
			reply(a.installModule(r.Context(), req.ID))
		} else {
			reply(a.removeModule(r.Context(), req.Name))
		}
	case strings.HasPrefix(p, "/api/v1/m/"):
		name, action, ok := strings.Cut(strings.TrimPrefix(p, "/api/v1/m/"), "/")
		if !ok || r.Method != "POST" {
			send(404, map[string]string{"error": "expected POST /api/v1/m/<module>/<action>"})
			return true
		}
		if body, ok := readJSON(65536); ok {
			reply(a.moduleAction(r.Context(), name, action, body))
		}
	default:
		return false
	}
	return true
}
