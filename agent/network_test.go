package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWiFiValidation(t *testing.T) {
	for _, raw := range []string{`{"ssid":"Home","password":"abcdefgh","open":false}`, `{"ssid":"Дом","password":"","open":true}`} {
		if _, _, err := validateWiFi(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{"ssid":"","open":true}`, `{"ssid":"Home","password":"short"}`, `{"ssid":"Home","password":"abcdefgh","open":true}`, `{"ssid":"Home\nBAD","open":true}`, `{"ssid":"Home","password":"abcdefg\\"}`, `{"ssid":"Home","password":"abcdefgh","path":"/etc/private"}`} {
		if _, _, err := validateWiFi(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestWiFiStoredConfig(t *testing.T) {
	if got := string(wifiConfig("Home", "abcdefgh")); !strings.Contains(got, "#pwd_b64=YWJjZGVmZ2g=") || !strings.Contains(got, "key_mgmt=WPA") {
		t.Fatal(got)
	}
	if got := string(wifiConfig("Open", "")); strings.Contains(got, "psk=") || !strings.Contains(got, "key_mgmt=NONE") {
		t.Fatal(got)
	}
}
func TestWiFiRequiresControlAndNativeAdapter(t *testing.T) {
	a := testAgent(t)
	a.cfg.AllowControl = false
	if _, err := a.call(context.Background(), request{Method: "local.wifi_setup", Params: json.RawMessage(`{"ssid":"Home","open":true}`)}); err == nil {
		t.Fatal("disabled control accepted")
	}
	a.cfg.AllowControl = true
	if _, err := a.call(context.Background(), request{Method: "local.wifi_setup", Params: json.RawMessage(`{"ssid":"Home","open":true}`)}); err == nil {
		t.Fatal("missing firmware adapter accepted")
	}
}
func TestRegionGuardModePersistence(t *testing.T) {
	a := testAgent(t)
	_ = os.MkdirAll(a.path("/data/dreame-local"), 0700)
	for _, enabled := range []bool{true, false} {
		raw, _ := json.Marshal(map[string]bool{"enabled": enabled})
		v, err := a.call(context.Background(), request{Method: "local.region_guard", Params: raw})
		if err != nil {
			t.Fatal(err)
		}
		if v.(map[string]any)["enabled"] != enabled {
			t.Fatal(v)
		}
	}
}
func TestGuardLoadedMarkerNeedsLiveMapping(t *testing.T) {
	a := testAgent(t)
	_ = os.MkdirAll(a.path("/tmp"), 0700)
	_ = os.WriteFile(a.path("/tmp/dreame-region-guard.loaded"), []byte("123\n"), 0600)
	if a.guardStatus()["hook_loaded"] != false {
		t.Fatal("stale PID accepted")
	}
	_ = os.MkdirAll(a.path("/proc/123"), 0700)
	_ = os.WriteFile(a.path("/proc/123/maps"), []byte("/data/dreame-local/region-guard.so\n"), 0600)
	if a.guardStatus()["hook_loaded"] != true {
		t.Fatal("mapping not detected")
	}
}
func TestPortalDNSPreservesDHCP(t *testing.T) {
	source := []byte("interface=wlan0\ndhcp-range=192.168.5.10,192.168.5.100,12h\n")
	got, err := portalDNSConfig(source)
	if err != nil || !strings.HasPrefix(string(got), string(source)) || !strings.Contains(string(got), "address=/#/192.168.5.1") {
		t.Fatal(string(got), err)
	}
	if _, err := portalDNSConfig([]byte("unrecognized")); err == nil {
		t.Fatal("unknown DNS schema accepted")
	}
}
func TestPortalProbeRedirects(t *testing.T) {
	for _, path := range []string{"/generate_204", "/hotspot-detect.html", "/connecttest.txt", "/"} {
		w := httptest.NewRecorder()
		portalRedirect(8765).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 302 || w.Header().Get("Location") != "http://192.168.5.1:8765/?setup=wifi" {
			t.Fatal(w.Code, w.Header())
		}
	}
}
func TestPrivateAtomicFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := atomicPrivate(p, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := atomicPrivate(p, []byte("second")); err != nil {
		t.Fatal(err)
	}
	s, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	if s.Mode().Perm() != 0600 || string(b) != "second" {
		t.Fatal(s.Mode(), string(b))
	}
}
func TestNativeWiFiJobSuccessAndRecovery(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			a := testAgent(t)
			_ = os.MkdirAll(a.path("/data/dreame-local"), 0700)
			directory := a.path("/data/config/dmio")
			path := filepath.Join(directory, "wifi.conf")
			original := []byte("ssid=\"Previous\"\nkey_mgmt=NONE\n")
			_ = os.WriteFile(path, original, 0600)
			script := filepath.Join(t.TempDir(), "wifi_start.sh")
			marker := script + ".recovered"
			source := "#!/bin/sh\n# select_master_ssid get_master_network\n[ \"$#\" = 1 ] || exit 3\nif [ \"$1\" = SET_AP_MODE ]; then touch '" + marker + "'; fi\nexit 0\n"
			_ = os.WriteFile(script, []byte(source), 0700)
			cli := filepath.Join(filepath.Dir(script), "wpa_cli")
			status := "wpa_state=DISCONNECTED"
			if success {
				status = "wpa_state=COMPLETED\\nip_address=192.168.1.20\\nssid=Home"
			}
			_ = os.WriteFile(cli, []byte("#!/bin/sh\nprintf '"+status+"\\n'\n"), 0700)
			t.Setenv("PATH", filepath.Dir(script)+":"+os.Getenv("PATH"))
			a.runWiFi(script, "Home", "abcdefgh")
			data, _ := os.ReadFile(path)
			backup, _ := os.ReadFile(a.path("/data/dreame-local/wifi.conf.before-agent"))
			if string(backup) != string(original) {
				t.Fatal("backup mismatch")
			}
			if success {
				if a.network.State != "connected" || !strings.Contains(string(data), "ssid=\"Home\"") {
					t.Fatal(a.network.State, string(data))
				}
			} else {
				if a.network.State != "failed" || string(data) != string(original) {
					t.Fatal(a.network.State, string(data))
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatal("AP recovery not requested")
				}
			}
		})
	}
}
