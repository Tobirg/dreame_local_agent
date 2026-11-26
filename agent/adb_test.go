package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
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

func testADBKey(t *testing.T, comment string) string {
	raw := make([]byte, adbKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(raw, 64)
	return base64.StdEncoding.EncodeToString(raw) + " " + comment
}

// fakeProc adds /proc/<pid> with an exe link and an environment.
func fakeProc(t *testing.T, a *agent, pid int, exe, env string) {
	dir := a.path("/proc/" + strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(env), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestADBKeysAndSwitch(t *testing.T) {
	a := testAgent(t)
	a.adb.wait = 300 * time.Millisecond
	pc, phone := testADBKey(t, "pavel@PashaPC"), testADBKey(t, "phone")
	keys := a.path(adbKeysPath)
	if err := os.MkdirAll(filepath.Dir(keys), 0700); err != nil {
		t.Fatal(err)
	}
	original := pc + "\n# a note the agent keeps\n" + phone + "\n"
	if err := os.WriteFile(keys, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	inode := func() uint64 { st, _ := os.Stat(keys); return st.Sys().(*syscall.Stat_t).Ino }
	before := inode()
	server := httptest.NewServer(http.HandlerFunc(a.handler))
	defer server.Close()
	do := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v map[string]any
		_ = json.NewDecoder(res.Body).Decode(&v)
		return res.StatusCode, v
	}
	ids := func(v map[string]any) []string {
		out := []string{}
		for _, k := range v["result"].(map[string]any)["keys"].([]any) {
			out = append(out, k.(map[string]any)["comment"].(string))
		}
		return out
	}
	code, v := do("GET", "/api/v1/system/adb", "")
	if code != 200 || strings.Join(ids(v), ",") != "pavel@PashaPC,phone" || v["result"].(map[string]any)["editable"] != true || v["result"].(map[string]any)["running"] != false {
		t.Fatal(code, v)
	}
	first := v["result"].(map[string]any)["keys"].([]any)[0].(map[string]any)["id"].(string)
	second := v["result"].(map[string]any)["keys"].([]any)[1].(map[string]any)["id"].(string)
	// A read token reaches no /api/v1/system route.
	a.configPath = filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(a.configPath, []byte(`{"token":"`+a.cfg.Token+`"}`), 0600)
	read, err := a.createToken("ha", "read")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/system/adb", nil)
	req.Header.Set("Authorization", "Bearer "+read["token"])
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatal("read token:", res.StatusCode)
	}
	for _, bad := range []string{"", "not-a-key", base64.StdEncoding.EncodeToString(make([]byte, 100)), testADBKey(t, "bad;comment"), pc} {
		b, _ := json.Marshal(map[string]string{"key": bad})
		if code, _ := do("POST", "/api/v1/system/adb/keys", string(b)); code != 400 {
			t.Fatalf("%.20q: %d", bad, code)
		}
	}
	tablet := testADBKey(t, "tablet")
	b, _ := json.Marshal(map[string]string{"key": tablet})
	if code, v := do("POST", "/api/v1/system/adb/keys", string(b)); code != 200 || strings.Join(ids(v), ",") != "pavel@PashaPC,phone,tablet" {
		t.Fatal(code, v)
	}
	if code, _ := do("DELETE", "/api/v1/system/adb/keys/"+second, ""); code != 200 {
		t.Fatal("remove", code)
	}
	if code, _ := do("DELETE", "/api/v1/system/adb/keys/"+second, ""); code != 400 {
		t.Fatal("remove twice", code)
	}
	got, _ := os.ReadFile(keys)
	if string(got) != pc+"\n# a note the agent keeps\n"+tablet+"\n" || inode() != before {
		t.Fatalf("key file %q, rewritten in place: %v", got, inode() == before)
	}
	if saved, _ := os.ReadFile(a.path(adbKeysBackup)); string(saved) != original {
		t.Fatal("backup:", string(saved))
	}
	// The last key stays.
	_, v = do("GET", "/api/v1/system/adb", "")
	third := v["result"].(map[string]any)["keys"].([]any)[1].(map[string]any)["id"].(string)
	if code, _ := do("DELETE", "/api/v1/system/adb/keys/"+third, ""); code != 200 {
		t.Fatal(code)
	}
	if code, v := do("DELETE", "/api/v1/system/adb/keys/"+first, ""); code != 400 || !strings.Contains(v["error"].(string), "last") {
		t.Fatal("last key removed:", code, v)
	}

	// Switch: stop kills the auth service before adbd; start brings adbd back only with key checking.
	fakeProc(t, a, 3000, "/usr/bin/adbd_auth_service", "")
	fakeProc(t, a, 3001, "/bin/adbd", "ADB_AUTH_ENABLE=1\x00ADB_TRANSPORT_PORT=4567\x00")
	signals := []string{}
	a.adb.signal = func(pid int, sig syscall.Signal) error {
		signals = append(signals, strconv.Itoa(pid)+":"+sig.String())
		return os.RemoveAll(a.path("/proc/" + strconv.Itoa(pid)))
	}
	if _, v := do("GET", "/api/v1/system/adb", ""); v["result"].(map[string]any)["running"] != true || v["result"].(map[string]any)["auth"] != true {
		t.Fatal(v)
	}
	if code, v := do("POST", "/api/v1/system/adb", `{"enabled":false}`); code != 200 || v["result"].(map[string]any)["running"] != false || strings.Join(signals, ",") != "3000:terminated,3001:terminated" {
		t.Fatal(code, v, signals)
	}
	env := "ADB_AUTH_ENABLE=1\x00"
	a.adb.start = func() error { fakeProc(t, a, 4001, "/bin/adbd", env); return nil }
	if code, v := do("POST", "/api/v1/system/adb", `{"enabled":true}`); code != 200 || v["result"].(map[string]any)["running"] != true {
		t.Fatal(code, v)
	}
	_ = os.RemoveAll(a.path("/proc/4001"))
	env = "ADB_TRANSPORT_PORT=4567\x00"
	if code, v := do("POST", "/api/v1/system/adb", `{"enabled":true}`); code != 500 || !strings.Contains(v["error"].(string), "without key checking") {
		t.Fatal("adbd without key checking kept:", code, v)
	}
	if adbd, _ := a.adbPIDs(); len(adbd) != 0 {
		t.Fatal("adbd without key checking still running")
	}
	a.adb.start = func() error { return nil }
	if code, _ := do("POST", "/api/v1/system/adb", `{"enabled":true}`); code != 500 {
		t.Fatal("adbd that never started:", code)
	}
}

func TestADBKeysWithoutBootHookAreReadOnly(t *testing.T) {
	a := testAgent(t)
	if err := os.MkdirAll(a.path("/etc"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.path(adbActiveKeys), []byte(testADBKey(t, "factory")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	keys, editable, err := a.adbKeys()
	if err != nil || editable || len(keys) != 1 || keys[0].Comment != "factory" {
		t.Fatal(keys, editable, err)
	}
	if err := a.addADBKey(testADBKey(t, "x")); err == nil {
		t.Fatal("edited the factory key file")
	}
}
