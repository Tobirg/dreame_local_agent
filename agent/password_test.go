package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChangePassword(t *testing.T) {
	a := testAgent(t)
	old := a.cfg.Token
	a.configPath = filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(a.configPath, []byte(`{"token":"`+old+`","port":8765,"custom":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	moduleBefore := a.moduleToken("demo")
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	do := func(method, path, token, body string) int {
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	change := func(token, current, next string) int {
		b, _ := json.Marshal(map[string]string{"current": current, "new": next})
		return do("POST", "/api/v1/system/password", token, string(b))
	}
	if code := change(old, "wrong-password", "kitchen-robot"); code != 403 {
		t.Fatal("wrong current password:", code)
	}
	for _, bad := range []string{"short", " leading-space", "trailing-space ", "пароль-кириллицей", strings.Repeat("x", 129)} {
		if code := change(old, old, bad); code != 400 {
			t.Fatalf("%q: %d", bad, code)
		}
	}
	if a.cfg.Token != old {
		t.Fatal("password changed by a refused request")
	}
	// A read token cannot change the password.
	read, err := a.createToken("ha", "read")
	if err != nil {
		t.Fatal(err)
	}
	if code := change(read["token"], old, "kitchen-robot"); code != 403 {
		t.Fatal("read token:", code)
	}
	// A config that cannot be written keeps the old password working.
	saved := a.configPath
	a.configPath = filepath.Join(t.TempDir(), "missing", "config.json")
	if code := change(old, old, "kitchen-robot"); code != 500 || do("GET", "/api/v1/state", old, "") != 200 {
		t.Fatal("failed write lost the old password:", code)
	}
	a.configPath = saved
	if code := change(old, old, "kitchen-robot"); code != 200 {
		t.Fatal("change:", code)
	}
	if do("GET", "/api/v1/state", old, "") != 401 || do("GET", "/api/v1/state", "kitchen-robot", "") != 200 {
		t.Fatal("new password is not the one in use")
	}
	if do("GET", "/api/v1/state", read["token"], "") != 200 {
		t.Fatal("scoped token lost after a password change")
	}
	if a.moduleToken("demo") != moduleBefore {
		t.Fatal("module token changed with the password")
	}
	var file map[string]any
	b, _ := os.ReadFile(a.configPath)
	if json.Unmarshal(b, &file) != nil || file["token"] != "kitchen-robot" || file["module_secret"] != old || file["custom"] != float64(1) || file["tokens"] == nil {
		t.Fatal("config file:", string(b))
	}
	// A second change keeps the module secret from the first one.
	if code := change("kitchen-robot", "kitchen-robot", "garden-robot"); code != 200 || a.cfg.ModuleSecret != old || a.moduleToken("demo") != moduleBefore {
		t.Fatal("second change:", code)
	}
}

func TestWrongPasswordsAreSlowed(t *testing.T) {
	a := testAgent(t)
	a.authFailures, a.authDelay = make(chan struct{}, 1), 200*time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	get := func(token string) int {
		req, _ := http.NewRequest("GET", server.URL+"/api/v1/state", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	start := time.Now()
	if get("wrong-password") != 401 || time.Since(start) < 200*time.Millisecond {
		t.Fatal("a wrong password was not held")
	}
	var wg sync.WaitGroup
	codes := make(chan int, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- get("wrong-password") }()
	}
	time.Sleep(50 * time.Millisecond)
	start = time.Now()
	if get(a.cfg.Token) != 200 || time.Since(start) > 100*time.Millisecond {
		t.Fatal("the right password was slowed by wrong ones")
	}
	wg.Wait()
	close(codes)
	limited := 0
	for c := range codes {
		if c == 429 {
			limited++
		}
	}
	if limited < 3 {
		t.Fatal("wrong passwords at once were not limited:", limited)
	}
}
