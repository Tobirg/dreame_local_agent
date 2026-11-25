package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScopedTokens(t *testing.T) {
	a := testAgent(t)
	a.configPath = filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(a.configPath, []byte(`{"token":"`+a.cfg.Token+`","port":8765,"custom":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	do := func(method, path, token, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v map[string]any
		_ = json.NewDecoder(res.Body).Decode(&v)
		return res.StatusCode, v
	}
	create := func(name, scope string) string {
		code, v := do("POST", "/api/v1/system/tokens", a.cfg.Token, `{"name":"`+name+`","scope":"`+scope+`"}`)
		if code != 200 {
			t.Fatal(code, v)
		}
		return v["result"].(map[string]any)["token"].(string)
	}
	read, control := create("dash", "read"), create("ha", "control")
	if code, _ := do("POST", "/api/v1/system/tokens", a.cfg.Token, `{"name":"ha","scope":"read"}`); code != 400 {
		t.Fatal("duplicate name accepted")
	}
	if code, _ := do("POST", "/api/v1/system/tokens", a.cfg.Token, `{"name":"x","scope":"admin"}`); code != 400 {
		t.Fatal("admin scope accepted")
	}
	saved, _ := os.ReadFile(a.configPath)
	if strings.Contains(string(saved), read) || !strings.Contains(string(saved), `"custom": 1`) || !strings.Contains(string(saved), `"dash"`) {
		t.Fatal("config must keep other fields and store only hashes", string(saved))
	}
	command := `{"name":"locate"}`
	cases := []struct {
		method, path, token, body string
		want                      int
	}{
		{"GET", "/api/v1/state", read, "", 200},
		{"GET", "/api/v1/ui", read, "", 200},
		{"POST", "/api/v1/ui", control, `{"label_size":14}`, 403},
		{"POST", "/api/v1/ui", a.cfg.Token, `{"label_size":7}`, 400},
		{"POST", "/api/v1/ui", a.cfg.Token, `{"label_size":16}`, 200},
		{"GET", "/api/v1/logs", control, "", 403},
		{"POST", "/rpc", read, `{"method":"get_properties","params":[{"siid":3,"piid":1}]}`, 200},
		{"POST", "/rpc", read, `{"method":"action","params":{"siid":7,"aiid":1}}`, 403},
		{"POST", "/api/v1/command", read, command, 403},
		{"POST", "/api/v1/command", control, command, 200},
		{"POST", "/rpc", control, `{"method":"local.reboot","params":null}`, 403},
		{"GET", "/api/v1/system/tokens", control, "", 403},
		{"GET", "/api/v1/modules", control, "", 403},
		{"POST", "/api/v1/system/update/apply", control, `{}`, 403},
		{"GET", "/api/v1/system/tokens", "wrong", "", 401},
		{"GET", "/api/v1/pets/photos", read, "", 200},
		{"GET", "/api/v1/pets/photos", "wrong", "", 401},
		{"POST", "/api/v1/pets/delete", read, `{"name":"pet-1-x.gif"}`, 403},
		{"POST", "/api/v1/pets/delete", control, `{"name":"pet-1-x.gif"}`, 404},
	}
	for _, c := range cases {
		if code, v := do(c.method, c.path, c.token, c.body); code != c.want {
			t.Fatal(c.method, c.path, c.token[:4], code, v)
		}
	}
	if code, v := do("GET", "/api/v1/system/tokens", a.cfg.Token, ""); code != 200 || len(v["result"].([]any)) != 2 {
		t.Fatal(code, v)
	}
	if code, _ := do("DELETE", "/api/v1/system/tokens/dash", a.cfg.Token, ""); code != 200 {
		t.Fatal("revoke", code)
	}
	if code, _ := do("GET", "/api/v1/state", read, ""); code != 401 {
		t.Fatal("revoked token still works", code)
	}
	if code, _ := do("DELETE", "/api/v1/system/tokens/dash", a.cfg.Token, ""); code != 404 {
		t.Fatal("second revoke", code)
	}
}

func TestEventsStreamStateChanges(t *testing.T) {
	a := testAgent(t)
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(res.Status, res.Header)
	}
	lines := bufio.NewScanner(res.Body)
	next := func() snapshot {
		t.Helper()
		done := make(chan snapshot, 1)
		go func() {
			for lines.Scan() {
				if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
					var s snapshot
					_ = json.Unmarshal([]byte(data), &s)
					done <- s
					return
				}
			}
		}()
		select {
		case s := <-done:
			return s
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		return snapshot{}
	}
	if s := next(); !s.Stale || s.Seq != 0 {
		t.Fatal("first event must be the current snapshot", s)
	}
	a.dev.mu.Lock()
	a.dev.snap.State = map[string]any{"battery": 80}
	a.bumpLocked()
	a.dev.mu.Unlock()
	if s := next(); s.Seq != 1 || s.State["battery"] != float64(80) {
		t.Fatal("change not streamed", s)
	}
}
