package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// apiToken is an extra token with limited rights; only its SHA-256 is stored.
type apiToken struct {
	Name    string `json:"name"`
	Scope   string `json:"scope"` // "read" or "control"
	SHA256  string `json:"sha256"`
	Created string `json:"created"`
}

const maxStreams = 8

var tokenName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// scopeOf returns "admin" for the main token, the scope of a scoped token, or "".
func (a *agent) scopeOf(auth string) string {
	if subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+a.password())) == 1 {
		return "admin"
	}
	presented, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || presented == "" {
		return ""
	}
	if name := a.sessionAccount(presented); name != "" {
		return a.roleOf(name) // "admin" or "user"
	}
	sum := sha256.Sum256([]byte(presented))
	hash := hex.EncodeToString(sum[:])
	a.tokensMu.RLock()
	defer a.tokensMu.RUnlock()
	scope := ""
	for _, t := range a.cfg.Tokens {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(t.SHA256)) == 1 {
			scope = t.Scope
		}
	}
	return scope
}

// limitedRoute says whether a read/control/module token may use this request at all.
// /rpc is let through here and narrowed to read methods after the body is parsed.
func limitedRoute(scope string, r *http.Request) bool {
	p := r.URL.Path
	switch {
	case r.Method == "GET" && (p == "/api/v1/state" || p == "/api/v1/capabilities" || p == "/api/v1/events" || p == "/api/v1/history" || p == "/api/v1/history/photo" || p == "/api/v1/map.svg" || p == "/api/v1/settings" || p == "/api/v1/map" || p == "/api/v1/ui"):
		return true
	case scope == "module":
		return r.Method == "POST" && p == "/api/v1/firmware_message"
	case p == "/api/v1/pets/photos" || p == "/api/v1/pets/photo":
		return r.Method == "GET" // pet photos are private: read and control tokens, not modules
	case p == "/api/v1/pets/delete":
		return scope == "control"
	case p == "/rpc":
		return true
	case p == "/api/v1/command":
		return scope == "control"
	case p == "/api/v1/camera/stream":
		return scope == "control" && r.Method == "GET" // live picture: like driving, not for read tokens
	}
	return false
}

func (a *agent) listTokens() []map[string]string {
	a.tokensMu.RLock()
	defer a.tokensMu.RUnlock()
	out := []map[string]string{}
	for _, t := range a.cfg.Tokens {
		out = append(out, map[string]string{"name": t.Name, "scope": t.Scope, "created": t.Created})
	}
	return out
}

// saveTokens writes the token list into the config file, keeping every other field as it is.
func (a *agent) saveTokens(tokens []apiToken) error {
	return a.saveConfigField("tokens", tokens)
}

// saveConfigField writes one field of the config file, keeping every other field as it is.
func (a *agent) saveConfigField(key string, value any) error {
	return a.saveConfigFields(map[string]any{key: value})
}

// saveConfigFields writes several fields of the config file in one atomic replace, keeping the others.
func (a *agent) saveConfigFields(fields map[string]any) error {
	if a.configPath == "" {
		return errors.New("agent was started without a configuration path")
	}
	b, err := readLimited(a.configPath, 65536)
	if err != nil {
		return errors.New("cannot read configuration")
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return errors.New("invalid configuration")
	}
	for key, value := range fields {
		if raw[key], err = json.Marshal(value); err != nil {
			return err
		}
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return atomicPrivate(a.configPath, append(out, '\n'))
}

func (a *agent) createToken(name, scope string) (map[string]string, error) {
	if !tokenName.MatchString(name) {
		return nil, errors.New("token name: 1-32 letters, digits, '.', '_' or '-'")
	}
	if scope != "read" && scope != "control" {
		return nil, errors.New("scope must be read or control")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(secret)
	sum := sha256.Sum256([]byte(token))
	a.tokensMu.Lock()
	defer a.tokensMu.Unlock()
	for _, t := range a.cfg.Tokens {
		if t.Name == name {
			return nil, fmt.Errorf("token %q already exists", name)
		}
	}
	created := time.Now().UTC().Format(time.RFC3339)
	tokens := append(append([]apiToken(nil), a.cfg.Tokens...), apiToken{Name: name, Scope: scope, SHA256: hex.EncodeToString(sum[:]), Created: created})
	if err := a.saveTokens(tokens); err != nil {
		return nil, err
	}
	a.cfg.Tokens = tokens
	// The token itself is shown once and never stored.
	return map[string]string{"name": name, "scope": scope, "created": created, "token": token}, nil
}

func (a *agent) revokeToken(name string) error {
	a.tokensMu.Lock()
	defer a.tokensMu.Unlock()
	var tokens []apiToken
	for _, t := range a.cfg.Tokens {
		if t.Name != name {
			tokens = append(tokens, t)
		}
	}
	if len(tokens) == len(a.cfg.Tokens) {
		return fmt.Errorf("no token %q", name)
	}
	if err := a.saveTokens(tokens); err != nil {
		return err
	}
	a.cfg.Tokens = tokens
	return nil
}

func (a *agent) tokenRoutes(w http.ResponseWriter, r *http.Request, send func(int, any)) bool {
	p := r.URL.Path
	if p == "/api/v1/system/embed" {
		a.embedRoute(w, r, send)
		return true
	}
	if p == "/api/v1/system/video_code" {
		a.videoCodeRoute(w, r, send)
		return true
	}
	if p == "/api/v1/system/video_cloud" {
		a.videoCloudRoute(w, r, send)
		return true
	}
	if p == "/api/v1/system/adb" || strings.HasPrefix(p, "/api/v1/system/adb/") {
		a.adbRoute(r, send)
		return true
	}
	if p == "/api/v1/system/password" {
		a.passwordRoute(r, send)
		return true
	}
	if p == "/api/v1/ui" {
		a.uiRoute(w, r, send)
		return true
	}
	if p != "/api/v1/system/tokens" && !strings.HasPrefix(p, "/api/v1/system/tokens/") {
		return false
	}
	switch {
	case p == "/api/v1/system/tokens" && r.Method == "GET":
		send(200, map[string]any{"result": a.listTokens()})
	case p == "/api/v1/system/tokens" && r.Method == "POST":
		var req struct {
			Name  string `json:"name"`
			Scope string `json:"scope"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
			send(400, map[string]string{"error": "invalid token request"})
			return true
		}
		t, err := a.createToken(req.Name, req.Scope)
		if err != nil {
			send(400, map[string]string{"error": err.Error()})
			return true
		}
		send(200, map[string]any{"result": t})
	case r.Method == "DELETE":
		if err := a.revokeToken(strings.TrimPrefix(p, "/api/v1/system/tokens/")); err != nil {
			send(404, map[string]string{"error": err.Error()})
			return true
		}
		send(200, map[string]any{"result": map[string]bool{"revoked": true}})
	default:
		send(405, map[string]string{"error": "method not allowed"})
	}
	return true
}

// watch returns the current snapshot and a channel closed on its next change.
func (a *agent) watch() (snapshot, <-chan struct{}) {
	s := a.snapshot()
	a.dev.mu.Lock()
	defer a.dev.mu.Unlock()
	if a.dev.changed == nil {
		a.dev.changed = make(chan struct{})
	}
	return s, a.dev.changed
}

// bumpLocked marks a new snapshot and wakes every event stream; a.dev.mu must be held.
func (a *agent) bumpLocked() {
	a.dev.snap.Seq++
	if a.dev.changed != nil {
		close(a.dev.changed)
		a.dev.changed = nil
	}
}

// serveEvents streams every state change as Server-Sent Events ("event: state").
func (a *agent) serveEvents(w http.ResponseWriter, r *http.Request, send func(int, any)) {
	if a.dev.streams.Add(1) > maxStreams {
		a.dev.streams.Add(-1)
		send(503, map[string]string{"error": "too many event streams"})
		return
	}
	defer a.dev.streams.Add(-1)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // the server's write timeout would cut the stream
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	var last uint64
	first := true
	for {
		s, changed := a.watch()
		if first || s.Seq != last {
			b, _ := json.Marshal(s)
			if _, err := fmt.Fprintf(w, "id: %d\nevent: state\ndata: %s\n\n", s.Seq, b); err != nil {
				return
			}
			first, last = false, s.Seq
		}
		if rc.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
		}
	}
}

// embedRoute reads and sets the origins that may show the panel in a frame (main token only: scoped tokens
// reach no /api/v1/system route).
func (a *agent) embedRoute(w http.ResponseWriter, r *http.Request, send func(int, any)) {
	switch r.Method {
	case "GET":
		a.tokensMu.RLock()
		list := append([]string{}, a.cfg.EmbedOrigins...)
		a.tokensMu.RUnlock()
		send(200, map[string]any{"result": map[string]any{"origins": list}})
	case "POST":
		var req struct {
			Origins []string `json:"origins"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req) != nil || len(req.Origins) > 20 {
			send(400, map[string]string{"error": "invalid request"})
			return
		}
		for _, o := range req.Origins {
			if !originRe.MatchString(o) {
				send(400, map[string]string{"error": "address must look like https://ha.home:8123 (scheme, host, optional port, no path): " + o})
				return
			}
		}
		a.tokensMu.Lock()
		defer a.tokensMu.Unlock()
		if err := a.saveConfigField("embed_origins", req.Origins); err != nil {
			send(500, map[string]string{"error": err.Error()})
			return
		}
		a.cfg.EmbedOrigins = req.Origins
		send(200, map[string]any{"result": map[string]any{"origins": req.Origins}})
	default:
		send(405, map[string]string{"error": "method not allowed"})
	}
}

// uiRoute keeps the panel's look on the agent, so every device shows the map the same way: any token reads it,
// only the main token changes it (POST is not a limited route).
func (a *agent) uiRoute(w http.ResponseWriter, r *http.Request, send func(int, any)) {
	switch r.Method {
	case "GET":
		a.tokensMu.RLock()
		size := a.cfg.LabelSize
		a.tokensMu.RUnlock()
		send(200, map[string]any{"result": map[string]any{"label_size": size}})
	case "POST":
		var req struct {
			LabelSize *int `json:"label_size"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req) != nil || req.LabelSize == nil || *req.LabelSize != 0 && (*req.LabelSize < 8 || *req.LabelSize > 32) {
			send(400, map[string]string{"error": "label_size: 0 (automatic) or 8..32"})
			return
		}
		a.tokensMu.Lock()
		defer a.tokensMu.Unlock()
		if err := a.saveConfigField("label_size", *req.LabelSize); err != nil {
			send(500, map[string]string{"error": err.Error()})
			return
		}
		a.cfg.LabelSize = *req.LabelSize
		send(200, map[string]any{"result": map[string]any{"label_size": *req.LabelSize}})
	default:
		send(405, map[string]string{"error": "method not allowed"})
	}
}
