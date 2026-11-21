package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary doubles as a fake agent: with DREAME_FAKE_AGENT_VERSION set,
// "<test binary> --version" prints that version like the real agent does.
func TestMain(m *testing.M) {
	if v := os.Getenv("DREAME_FAKE_AGENT_VERSION"); v != "" && len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("dreame-local-agent " + v)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func selfBinary(t *testing.T) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStageUpdateChecksExecutableAndVersion(t *testing.T) {
	a := testAgent(t)
	if err := os.MkdirAll(a.path(agentBase), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.stageUpdate(context.Background(), []byte("#!/bin/sh\necho hi\n")); err == nil {
		t.Fatal("script accepted as agent")
	}
	self := selfBinary(t)
	foreign := append([]byte(nil), self...)
	binary.LittleEndian.PutUint16(foreign[18:], 40) // e_machine = EM_ARM
	if _, err := a.stageUpdate(context.Background(), foreign); err == nil || !strings.Contains(err.Error(), "EM_ARM") {
		t.Fatal("foreign architecture accepted", err)
	}
	t.Setenv("DREAME_FAKE_AGENT_VERSION", "")
	if _, err := a.stageUpdate(context.Background(), self); err == nil {
		t.Fatal("binary without agent version accepted")
	}
	if _, err := os.Stat(a.agentFile("agent.update")); !os.IsNotExist(err) {
		t.Fatal("rejected binary left staged")
	}
	t.Setenv("DREAME_FAKE_AGENT_VERSION", "9.9.9")
	staged, err := a.stageUpdate(context.Background(), self)
	if err != nil {
		t.Fatal(err)
	}
	if staged["version"] != "9.9.9" || staged["current"] != version || len(staged["sha256"].(string)) != 64 {
		t.Fatal(staged)
	}
	if st := a.updateStatus(context.Background()); st["staged"] != "9.9.9" || st["previous"] != nil || st["applying"] != false {
		t.Fatal(st)
	}
	if _, err := a.startUpdate("other"); err == nil {
		t.Fatal("unknown source accepted")
	}
	if _, err := a.startUpdate("previous"); err == nil || !strings.Contains(err.Error(), "no previous") {
		t.Fatal("rollback without previous agent", err)
	}
}

// fakeUpdate prepares base with "current" and "staged" files whose content is their version.
func fakeUpdate(t *testing.T, healthy map[string]bool) (updater, *[]string) {
	t.Helper()
	base := t.TempDir()
	for name, v := range map[string]string{"dreame-local-agent": "1.0", "agent.update": "2.0"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(v), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var log []string
	current := filepath.Join(base, "dreame-local-agent")
	u := updater{base: base, source: filepath.Join(base, "agent.update"), id: "abc",
		versionOf: func(p string) (string, error) {
			b, err := os.ReadFile(p)
			return string(b), err
		},
		stop: func() error { log = append(log, "stop"); return nil },
		start: func() error {
			b, _ := os.ReadFile(current)
			log = append(log, "start "+string(b))
			return nil
		},
		healthy: func(v string) error {
			if healthy[v] {
				return nil
			}
			return errors.New("HTTP 502 robot identity mismatch")
		},
	}
	return u, &log
}

func read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestUpdaterSwapsAndKeepsPrevious(t *testing.T) {
	u, log := fakeUpdate(t, map[string]bool{"2.0": true})
	r := u.run()
	if r.Status != "updated" || r.From != "1.0" || r.To != "2.0" || r.ID != "abc" {
		t.Fatal(r)
	}
	if read(t, filepath.Join(u.base, "dreame-local-agent")) != "2.0" || read(t, filepath.Join(u.base, "dreame-local-agent.previous")) != "1.0" {
		t.Fatal("files not swapped")
	}
	if strings.Join(*log, ",") != "stop,start 2.0" || read(t, filepath.Join(u.base, "dreame-local-agent.previous.old")) != "<missing>" {
		t.Fatal(*log)
	}
	// Rolling back from .previous swaps the two binaries again.
	u.source = filepath.Join(u.base, "dreame-local-agent.previous")
	u.healthy = func(string) error { return nil }
	if r := u.run(); r.Status != "updated" || r.To != "1.0" || read(t, filepath.Join(u.base, "dreame-local-agent")) != "1.0" ||
		read(t, filepath.Join(u.base, "dreame-local-agent.previous")) != "2.0" {
		t.Fatal("manual rollback", r)
	}
}

func TestUpdaterRollsBackUnhealthyAgent(t *testing.T) {
	u, log := fakeUpdate(t, map[string]bool{"1.0": true})
	previous := filepath.Join(u.base, "dreame-local-agent.previous")
	if err := os.WriteFile(previous, []byte("0.5"), 0700); err != nil {
		t.Fatal(err)
	}
	r := u.run()
	if r.Status != "rolled_back" || !strings.Contains(r.Error, "identity mismatch") {
		t.Fatal(r)
	}
	// The earlier rollback target survives a failed update.
	if read(t, previous) != "0.5" || read(t, previous+".old") != "<missing>" {
		t.Fatal("rollback target lost", read(t, previous))
	}
	if read(t, filepath.Join(u.base, "dreame-local-agent")) != "1.0" || read(t, filepath.Join(u.base, "dreame-local-agent.failed")) != "2.0" {
		t.Fatal("old agent not restored")
	}
	if strings.Join(*log, ",") != "stop,start 2.0,stop,start 1.0" {
		t.Fatal(*log)
	}
	u, _ = fakeUpdate(t, map[string]bool{})
	if r := u.run(); r.Status != "failed" || !strings.Contains(r.Error, "did not come back") {
		t.Fatal("both versions down must be reported", r)
	}
	u, log = fakeUpdate(t, map[string]bool{"2.0": true})
	u.versionOf = func(string) (string, error) { return "", errors.New("file is not a Dreame Local agent") }
	if r := u.run(); r.Status != "failed" || len(*log) != 0 || read(t, filepath.Join(u.base, "dreame-local-agent")) != "1.0" {
		t.Fatal("unreadable staged agent must change nothing", r, *log)
	}
}

func TestUpdateRoutesNeedTokenAndControl(t *testing.T) {
	a := testAgent(t)
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	do := func(method, path string, auth bool) (int, map[string]any) {
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader("x"))
		if auth {
			req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
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
	if code, _ := do("GET", "/api/v1/system/update", false); code != 401 {
		t.Fatal("status without token", code)
	}
	if code, v := do("GET", "/api/v1/system/update", true); code != 200 || v["result"].(map[string]any)["current"] != version {
		t.Fatal(code, v)
	}
	a.cfg.AllowControl = false
	if code, _ := do("POST", "/api/v1/system/update/stage", true); code != 403 {
		t.Fatal("stage without allow_control", code)
	}
	if code, _ := do("POST", "/api/v1/system/update/apply", true); code != 403 {
		t.Fatal("apply without allow_control", code)
	}
}
