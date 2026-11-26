package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func servicePackage(t *testing.T, version string) []byte {
	return makePackage(t,
		pkgFile{name: "manifest.json", body: `{"name":"svc","version":"` + version + `","title":"Svc","handler":"handler","service":"handler","control":true}`},
		pkgFile{name: "handler", mode: 0755, body: `#!/bin/sh
D="$DREAME_MODULE_DATA"
case "$1" in
install) echo "${DREAME_MODULE_PREVIOUS:-none}" > "$D/previous" ;;
run) echo $$ > "$D/pid"; echo "$DREAME_MODULE_TOKEN" >> "$D/runs"; exec sleep 600 ;;
esac
echo '{}'
`})
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(8 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out waiting for", what)
}

func TestModuleServiceLifecycle(t *testing.T) {
	a := testAgent(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.modules.ctx = ctx
	defer a.stopServices()
	install := func(version string) {
		staged, err := a.stageModule(servicePackage(t, version))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.installModule(ctx, staged["id"].(string)); err != nil {
			t.Fatal(err)
		}
	}
	data := a.moduleDataDir("svc")
	runs := func() []string { b, _ := os.ReadFile(filepath.Join(data, "runs")); return strings.Fields(string(b)) }
	pid := func() int {
		b, _ := os.ReadFile(filepath.Join(data, "pid"))
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return n
	}
	alive := func(p int) bool { return p > 0 && syscall.Kill(p, 0) == nil }

	install("1.0.0")
	waitFor(t, "service start", func() bool { return len(runs()) == 1 && a.serviceRunning("svc") })
	if runs()[0] != a.moduleToken("svc") || len(runs()[0]) != 64 {
		t.Fatal("service must get the module token", runs())
	}
	if b, _ := os.ReadFile(filepath.Join(data, "previous")); strings.TrimSpace(string(b)) != "none" {
		t.Fatal("first install has no previous version", string(b))
	}
	// A crashed service is restarted.
	first := pid()
	_ = syscall.Kill(first, syscall.SIGKILL)
	waitFor(t, "service restart", func() bool { return len(runs()) == 2 && pid() != first && alive(pid()) })

	// An update stops the old process, keeps the data directory and sees the previous version.
	old := pid()
	install("1.0.1")
	waitFor(t, "service after update", func() bool { return len(runs()) == 3 && alive(pid()) && pid() != old })
	if alive(old) {
		t.Fatal("old service still running after update")
	}
	if b, _ := os.ReadFile(filepath.Join(data, "previous")); !strings.Contains(string(b), ".previous-svc") {
		t.Fatal("update must see the previous version directory", string(b))
	}

	// The module token may send commands because the manifest declares control.
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL+"/api/v1/command", strings.NewReader(`{"name":"locate"}`))
	req.Header.Set("Authorization", "Bearer "+a.moduleToken("svc"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("control module command", res.StatusCode)
	}

	last := pid()
	if _, err := a.removeModule(ctx, "svc"); err != nil {
		t.Fatal(err)
	}
	if alive(last) {
		t.Fatal("service still running after removal")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("data directory kept after removal")
	}
	if a.moduleForToken("Bearer "+a.moduleToken("svc")) != "" {
		t.Fatal("token of a removed module still accepted")
	}
}
