package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccounts(t *testing.T) {
	a := testAgent(t)
	a.configPath = filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(a.configPath, []byte(`{"token":"`+a.cfg.Token+`","custom":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	do := func(method, path, token, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v map[string]any
		_ = json.NewDecoder(res.Body).Decode(&v)
		return res.StatusCode, v
	}
	login := func(name, password string) (int, string) {
		b, _ := json.Marshal(map[string]string{"name": name, "password": password})
		code, v := do("POST", "/api/v1/login", "", string(b))
		if code != 200 {
			return code, ""
		}
		return code, v["result"].(map[string]any)["token"].(string)
	}
	// The admin account is the agent password; the raw password still works as a token.
	code, adminSession := login("admin", a.cfg.Token)
	if code != 200 || len(adminSession) != 64 {
		t.Fatal("admin login", code)
	}
	if code, _ := login("admin", "wrong-password"); code != 401 {
		t.Fatal("wrong admin password", code)
	}
	if code, v := do("GET", "/api/v1/account", adminSession, ""); code != 200 || v["result"].(map[string]any)["role"] != "admin" {
		t.Fatal(code, v)
	}
	// Admin creates a user.
	for _, bad := range []string{`{"name":"x y","role":"user","password":"long-enough"}`, `{"name":"bob","role":"root","password":"long-enough"}`, `{"name":"bob","role":"user","password":"short"}`, `{"name":"Admin","role":"user","password":"long-enough"}`} {
		if code, _ := do("POST", "/api/v1/system/accounts", adminSession, bad); code != 400 {
			t.Fatal(bad, code)
		}
	}
	if code, _ := do("POST", "/api/v1/system/accounts", adminSession, `{"name":"masha","role":"user","password":"masha-pass-1"}`); code != 200 {
		t.Fatal("create", code)
	}
	if code, _ := do("POST", "/api/v1/system/accounts", adminSession, `{"name":"masha","role":"user","password":"masha-pass-1"}`); code != 400 {
		t.Fatal("duplicate", code)
	}
	b, _ := os.ReadFile(a.configPath)
	if strings.Contains(string(b), "masha-pass-1") || !strings.Contains(string(b), `"custom": 1`) {
		t.Fatal("config:", string(b))
	}
	code, user := login("masha", "masha-pass-1")
	if code != 200 {
		t.Fatal("user login", code)
	}
	if code, _ := login("masha", "masha-pass-2"); code != 401 {
		t.Fatal("wrong user password", code)
	}
	if code, _ := login("nobody", "masha-pass-1"); code != 401 {
		t.Fatal("unknown account", code)
	}
	// What a user may and may not do.
	allowed := [][2]string{{"GET", "/api/v1/state"}, {"GET", "/api/v1/capabilities"}, {"GET", "/api/v1/account"}, {"GET", "/api/v1/ui"}, {"GET", "/api/v1/modules"}}
	for _, c := range allowed {
		if code, _ := do(c[0], c[1], user, ""); code == 401 || code == 403 {
			t.Fatal("user refused", c, code)
		}
	}
	denied := [][3]string{
		{"GET", "/api/v1/system/accounts", ""}, {"GET", "/api/v1/system/tokens", ""}, {"GET", "/api/v1/system/adb", ""},
		{"POST", "/api/v1/system/update/apply", `{"source":"staged"}`}, {"POST", "/api/v1/system/password", `{}`},
		{"GET", "/api/v1/logs", ""}, {"POST", "/api/v1/modules/remove", `{"name":"x"}`}, {"POST", "/api/v1/voice/remove", `{"id":"RU"}`},
		{"POST", "/api/v1/ui", `{"label_size":12}`},
		{"POST", "/rpc", `{"method":"local.reboot","params":null}`}, {"POST", "/rpc", `{"method":"local.wifi_setup","params":{"ssid":"x","password":"","open":true}}`},
	}
	for _, c := range denied {
		if code, _ := do(c[0], c[1], user, c[2]); code != 403 {
			t.Fatal("user allowed", c, code)
		}
	}
	// A user changes their own password; other sessions of that account end, this one stays.
	_, other := login("masha", "masha-pass-1")
	if code, _ := do("POST", "/api/v1/account/password", user, `{"current":"nope-nope","new":"masha-pass-2"}`); code != 403 {
		t.Fatal("wrong current", code)
	}
	if code, _ := do("POST", "/api/v1/account/password", user, `{"current":"masha-pass-1","new":"masha-pass-2"}`); code != 200 {
		t.Fatal("own password", code)
	}
	if code, _ := do("GET", "/api/v1/state", user, ""); code != 200 {
		t.Fatal("own session ended", code)
	}
	if code, _ := do("GET", "/api/v1/state", other, ""); code != 401 {
		t.Fatal("other session kept", code)
	}
	if code, _ := login("masha", "masha-pass-2"); code != 200 {
		t.Fatal("new password", code)
	}
	// Sessions survive an agent restart.
	b2 := testAgent(t)
	b2.configPath, b2.cfg = a.configPath, a.cfg
	if b2.sessionAccount(user) != "masha" {
		t.Fatal("session lost on restart")
	}
	// Removing the account ends its sessions; admin cannot be removed, nor the account in use.
	if code, _ := do("DELETE", "/api/v1/system/accounts/admin", adminSession, ""); code != 400 {
		t.Fatal("removed admin", code)
	}
	if code, _ := do("DELETE", "/api/v1/system/accounts/masha", adminSession, ""); code != 200 {
		t.Fatal("remove", code)
	}
	if code, _ := do("GET", "/api/v1/state", user, ""); code != 401 {
		t.Fatal("removed account still signed in", code)
	}
	// A second admin account has full access; it cannot remove itself.
	if code, _ := do("POST", "/api/v1/system/accounts", adminSession, `{"name":"pavel","role":"admin","password":"pavel-pass-1"}`); code != 200 {
		t.Fatal(code)
	}
	_, pavel := login("pavel", "pavel-pass-1")
	if code, _ := do("GET", "/api/v1/system/adb", pavel, ""); code == 403 || code == 401 {
		t.Fatal("second admin refused", code)
	}
	if code, _ := do("DELETE", "/api/v1/system/accounts/pavel", pavel, ""); code != 400 {
		t.Fatal("removed itself", code)
	}
	// Signing out ends the session.
	if code, _ := do("POST", "/api/v1/account/logout", pavel, ""); code != 200 {
		t.Fatal(code)
	}
	if code, _ := do("GET", "/api/v1/state", pavel, ""); code != 401 {
		t.Fatal("signed out session works", code)
	}
	// The admin changes the agent password through the account route; the raw password changes with it.
	if code, _ := do("POST", "/api/v1/account/password", adminSession, `{"current":"`+a.cfg.Token+`","new":"admin-pass-9"}`); code != 200 {
		t.Fatal("admin password", code)
	}
	if code, _ := do("GET", "/api/v1/state", "admin-pass-9", ""); code != 200 {
		t.Fatal("raw new password")
	}
	if code, _ := do("GET", "/api/v1/state", adminSession, ""); code != 200 {
		t.Fatal("admin session ended by its own change")
	}
}
