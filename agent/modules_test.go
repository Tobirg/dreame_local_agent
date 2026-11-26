package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pkgFile struct {
	name, body string
	mode       int64
	kind       byte
}

func makePackage(t *testing.T, files ...pkgFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		kind := f.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := f.mode
		if mode == 0 {
			mode = 0644
		}
		h := &tar.Header{Name: f.name, Mode: mode, Size: int64(len(f.body)), Typeflag: kind}
		if kind == tar.TypeSymlink {
			h.Size, h.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			_, _ = tw.Write([]byte(f.body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

const testManifest = `{"name":"demo","version":"1.0.0","title":"Demo","handler":"handler","panel":"panel.js","read_actions":["status"],"firmware_messages":["device_lock"]}`

// demoHandler logs every action into the modules dir (it runs from staging or its own dir);
// FAIL_CHECK in <root>/data and FAIL_INSTALL in the modules dir make those steps fail.
const demoHandler = `#!/bin/sh
B="${DREAME_MODULE_DIR%%/modules/*}/modules"
echo "$1" >> "$B/actions.log"
case "$1" in
check) [ -e "$B/../../FAIL_CHECK" ] && { echo '{"error":"firmware not supported"}'; exit 1; }; echo '{"ok":true}';;
install) [ -e "$B/FAIL_INSTALL" ] && { echo boom >&2; exit 3; }; echo '{}';;
status) printf '{"code":"%s","has_token":%s,"api":"%s"}' "$P_code" "$( [ -n "$DREAME_MODULE_TOKEN" ] && echo true || echo false )" "$DREAME_API";;
*) echo '{}';;
esac
`

func demoPackage(t *testing.T, version string) []byte {
	return makePackage(t,
		pkgFile{name: "./manifest.json", body: strings.Replace(testManifest, "1.0.0", version, 1)},
		pkgFile{name: "./handler", body: demoHandler, mode: 0755},
		pkgFile{name: "./panel.js", body: "export default function mount(el){el.textContent='demo'}"},
		pkgFile{name: "./boot-env.sh", body: ": demo\n"},
	)
}

func installDemo(t *testing.T, a *agent, version string) {
	t.Helper()
	staged, err := a.stageModule(demoPackage(t, version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.installModule(context.Background(), staged["id"].(string)); err != nil {
		t.Fatal(err)
	}
}

func TestStageRejectsUnsafePackages(t *testing.T) {
	a := testAgent(t)
	manifest := pkgFile{name: "manifest.json", body: `{"name":"demo","version":"1","title":"Demo"}`}
	for name, data := range map[string][]byte{
		"traversal":    makePackage(t, manifest, pkgFile{name: "../escape", body: "x"}),
		"absolute":     makePackage(t, manifest, pkgFile{name: "/etc/escape", body: "x"}),
		"symlink":      makePackage(t, manifest, pkgFile{name: "link", kind: tar.TypeSymlink}),
		"no manifest":  makePackage(t, pkgFile{name: "handler", body: "x"}),
		"bad name":     makePackage(t, pkgFile{name: "manifest.json", body: `{"name":"../x","version":"1","title":"X"}`}),
		"missing file": makePackage(t, pkgFile{name: "manifest.json", body: `{"name":"demo","version":"1","title":"Demo","handler":"nope"}`}),
		"not gzip":     []byte("plain text"),
	} {
		if _, err := a.stageModule(data); err == nil {
			t.Errorf("%s: package accepted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(a.root, "data/dreame-local/escape")); !os.IsNotExist(err) {
		t.Fatal("traversal wrote outside the module directory")
	}
	if len(a.listModules()) != 0 {
		t.Fatal("rejected package was installed")
	}
}

func TestInstallCheckSwapAndRollback(t *testing.T) {
	a := testAgent(t)
	staged, err := a.stageModule(demoPackage(t, "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if staged["boot_hook"] != true || len(staged["sha256"].(string)) != 64 || staged["installed_version"] != nil {
		t.Fatal(staged)
	}
	writeFile(t, a, "/data/FAIL_CHECK", "")
	if _, err := a.installModule(context.Background(), staged["id"].(string)); err == nil || !strings.Contains(err.Error(), "firmware not supported") {
		t.Fatal("failed check must block install", err)
	}
	os.Remove(a.path("/data/FAIL_CHECK"))
	installDemo(t, a, "1.0.0")
	list := a.listModules()
	if len(list) != 1 || list[0]["version"] != "1.0.0" || list[0]["boot_hook"] != true {
		t.Fatal(list)
	}
	// A failing install step restores the previous version.
	writeFile(t, a, modulesBase+"/FAIL_INSTALL", "")
	staged, _ = a.stageModule(demoPackage(t, "2.0.0"))
	if staged["installed_version"] != "1.0.0" {
		t.Fatal(staged)
	}
	if _, err := a.installModule(context.Background(), staged["id"].(string)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatal("install failure must surface handler stderr", err)
	}
	if v := a.listModules()[0]["version"]; v != "1.0.0" {
		t.Fatal("previous version not restored", v)
	}
	os.Remove(a.path(modulesBase + "/FAIL_INSTALL"))
	if _, err := a.removeModule(context.Background(), "demo"); err != nil || len(a.listModules()) != 0 {
		t.Fatal("remove failed", err)
	}
	log, _ := os.ReadFile(a.path(modulesBase + "/actions.log"))
	if got := strings.Fields(string(log)); strings.Join(got, ",") != "check,check,install,check,install,uninstall" {
		t.Fatal("unexpected lifecycle", got)
	}
}

func TestModuleActionsAndControl(t *testing.T) {
	a := testAgent(t)
	installDemo(t, a, "1.0.0")
	v, err := a.moduleAction(context.Background(), "demo", "status", []byte(`{"code":200,"nested":{"x":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(v.(json.RawMessage), &out)
	if out["code"] != "200" || out["has_token"] != true || !strings.HasPrefix(out["api"].(string), "http://127.0.0.1:") {
		t.Fatal(out)
	}
	a.cfg.AllowControl = false
	if _, err := a.moduleAction(context.Background(), "demo", "status", nil); err != nil {
		t.Fatal("read action must work without control", err)
	}
	if _, err := a.moduleAction(context.Background(), "demo", "set", nil); err == nil {
		t.Fatal("write action allowed without control")
	}
	for _, bad := range [][2]string{{"nope", "status"}, {"demo", "../x"}, {"demo", "Status"}} {
		if _, err := a.moduleAction(context.Background(), bad[0], bad[1], nil); err == nil {
			t.Error("accepted", bad)
		}
	}
}

func TestFirmwareMessageOnlyDeclaredAndOnlyModuleToken(t *testing.T) {
	a := testAgent(t)
	argv := filepath.Join(a.root, "argv.json")
	writeFile(t, a, "/avacmd-msg", "#!/usr/bin/env python3\nimport json,sys\njson.dump(sys.argv[1:],open("+quotePython(argv)+",'w'))\n")
	_ = os.Chmod(a.path("/avacmd-msg"), 0700)
	a.avacmd = a.path("/avacmd-msg")
	installDemo(t, a, "1.0.0")
	token := a.moduleToken("demo")
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	post := func(path, auth, body string) (int, string) {
		req, _ := http.NewRequest("POST", server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+auth)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := post("/api/v1/firmware_message", token, `{"cmd":"device_lock","code":200}`); code != 200 || !strings.Contains(body, `"dispatched":true`) {
		t.Fatal(code, body)
	}
	b, _ := os.ReadFile(argv)
	var args []string
	_ = json.Unmarshal(b, &args)
	if len(args) != 2 || args[0] != "msg_cvt" || args[1] != `{"cmd":"device_lock","code":200,"type":"msgCvt"}` {
		t.Fatal("avacmd arguments", args)
	}
	os.Remove(argv)
	for _, body := range []string{`{"cmd":"download","url":"x"}`, `{"cmd":"device_lock","code":{"x":1}}`, `{"cmd":"device_lock","type":"other"}`, `[]`} {
		if code, _ := post("/api/v1/firmware_message", token, body); code == 200 {
			t.Error("accepted", body)
		}
	}
	if code, _ := post("/rpc", token, `{"method":"get_properties","params":[{"siid":3,"piid":1}]}`); code != 403 {
		t.Fatal("module token must not reach /rpc", code)
	}
	if code, _ := post("/api/v1/firmware_message", a.cfg.Token, `{"cmd":"device_lock","code":200}`); code == 200 {
		t.Fatal("firmware messages are for module tokens only")
	}
	if _, err := os.Stat(argv); !os.IsNotExist(err) {
		t.Fatal("rejected message reached avacmd")
	}
	a.cfg.AllowControl = false
	if code, _ := post("/api/v1/firmware_message", token, `{"cmd":"device_lock","code":200}`); code == 200 {
		t.Fatal("firmware message allowed without control")
	}
}

func TestModuleHTTPFlowAndPanel(t *testing.T) {
	a := testAgent(t)
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	do := func(method, path, contentType string, body []byte, auth bool) (int, map[string]any) {
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
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
	if code, _ := do("POST", "/api/v1/modules/stage", "application/gzip", demoPackage(t, "1.0.0"), false); code != 401 {
		t.Fatal("stage without token", code)
	}
	code, v := do("POST", "/api/v1/modules/stage", "application/gzip", demoPackage(t, "1.0.0"), true)
	if code != 200 {
		t.Fatal(code, v)
	}
	id := v["result"].(map[string]any)["id"].(string)
	if code, v := do("POST", "/api/v1/modules/install", "", []byte(`{"id":"`+id+`"}`), true); code != 200 {
		t.Fatal(code, v)
	}
	if code, v := do("GET", "/api/v1/modules", "", nil, true); code != 200 || len(v["result"].([]any)) != 1 {
		t.Fatal(code, v)
	}
	if code, v := do("POST", "/api/v1/m/demo/status", "", []byte(`{"code":"401"}`), true); code != 200 || v["result"].(map[string]any)["code"] != "401" {
		t.Fatal(code, v)
	}
	res, err := http.Get(server.URL + "/m/demo/panel.js")
	if err != nil || res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "javascript") {
		t.Fatal("panel", err, res.StatusCode)
	}
	if res, _ := http.Get(server.URL + "/m/nope/panel.js"); res.StatusCode != 404 {
		t.Fatal("unknown panel", res.StatusCode)
	}
	if code, _ := do("POST", "/api/v1/modules/remove", "", []byte(`{"name":"demo"}`), true); code != 200 || len(a.listModules()) != 0 {
		t.Fatal("remove", code)
	}
}

func TestCrossModuleReadNeedsControl(t *testing.T) {
	a := testAgent(t)
	installDemo(t, a, "1.0.0")
	for _, m := range []struct{ name, control string }{{"bridge", "true"}, {"plain", "false"}} {
		staged, err := a.stageModule(makePackage(t,
			pkgFile{name: "./manifest.json", body: `{"name":"` + m.name + `","version":"1.0.0","title":"X","handler":"handler","control":` + m.control + `}`},
			pkgFile{name: "./handler", body: demoHandler, mode: 0755}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.installModule(context.Background(), staged["id"].(string)); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	post := func(path, module string) int {
		req, _ := http.NewRequest("POST", server.URL+path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+a.moduleToken(module))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := post("/api/v1/m/demo/status", "bridge"); code != 200 {
		t.Fatal("control module must read another module", code)
	}
	for _, c := range [][2]string{{"/api/v1/m/demo/set", "bridge"}, {"/api/v1/m/demo/status", "plain"}, {"/api/v1/m/bridge/status", "bridge"}, {"/api/v1/modules", "bridge"}} {
		if code := post(c[0], c[1]); code != 403 {
			t.Error(c, code)
		}
	}
}
