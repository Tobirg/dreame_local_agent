package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type networkJob struct {
	mu    sync.Mutex
	State string
	Error string
}

func (a *agent) controlAllowed() bool {
	return a.cfg.AllowControl && a.cfg.ExpectedModel != "" && a.cfg.ExpectedDID != "" && a.cfg.ExpectedMAC != ""
}
func atomicPrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func (a *agent) guardStatus() map[string]any {
	_, enabledErr := os.Stat(a.path("/data/dreame-local/region-guard.enabled"))
	loaded := false
	if b, err := readLimited(a.path("/tmp/dreame-region-guard.loaded"), 64); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
			if maps, err := readLimited(a.path(fmt.Sprintf("/proc/%d/maps", pid)), 4<<20); err == nil {
				loaded = strings.Contains(string(maps), "/data/dreame-local/region-guard.so")
			}
		}
	}
	_, blockedErr := os.Stat(a.path("/tmp/dreame-region-guard.blocked"))
	return map[string]any{"enabled": enabledErr == nil, "hook_loaded": loaded, "blocked_since_boot": blockedErr == nil, "restart_required": !loaded}
}
func validateWiFi(raw json.RawMessage) (string, string, error) {
	var p struct {
		SSID     string `json:"ssid"`
		Password string `json:"password"`
		Open     bool   `json:"open"`
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return "", "", errors.New("invalid Wi-Fi settings")
	}
	if !utf8.ValidString(p.SSID) || len(p.SSID) < 1 || len(p.SSID) > 32 || strings.IndexFunc(p.SSID, unicode.IsControl) >= 0 {
		return "", "", errors.New("SSID must contain 1..32 UTF-8 bytes without control characters")
	}
	if p.Open {
		if p.Password != "" {
			return "", "", errors.New("open network must have no password")
		}
	} else {
		if len(p.Password) < 8 || len(p.Password) > 63 {
			return "", "", errors.New("WPA2 passphrase must contain 8..63 ASCII characters")
		}
		for _, c := range p.Password {
			if c < 32 || c > 126 || c == '"' || c == '\\' {
				return "", "", errors.New("this firmware Wi-Fi adapter does not support quotes, backslashes or non-ASCII characters in the passphrase")
			}
		}
	}
	return p.SSID, p.Password, nil
}
func wifiConfig(ssid, password string) []byte {
	s := "ssid=\"" + ssid + "\"\n"
	if password == "" {
		s += "key_mgmt=NONE\n"
	} else {
		s += "psk=\"" + password + "\"\n#pwd_b64=" + base64.StdEncoding.EncodeToString([]byte(password)) + "\nkey_mgmt=WPA\n"
	}
	return []byte(s)
}
func wifiSignal(status, signal string) (string, *int, *int) {
	ssid := ""
	connected := true
	var dbm *int
	for _, line := range strings.Split(status, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok && k == "wpa_state" && v != "COMPLETED" {
			connected = false
		}
		if ok && k == "ssid" {
			ssid = v
			if strings.Contains(v, "\\") {
				if decoded, err := strconv.Unquote("\"" + v + "\""); err == nil {
					ssid = decoded
				}
			}
		}
	}
	for _, line := range strings.Split(signal, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok && k == "RSSI" {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n < 0 && n >= -150 {
				value := n
				dbm = &value
			}
		}
	}
	if !connected {
		return "", nil, nil
	}
	if dbm == nil {
		return ssid, nil, nil
	}
	quality := 2 * (*dbm + 100)
	if quality < 0 {
		quality = 0
	}
	if quality > 100 {
		quality = 100
	}
	return ssid, dbm, &quality
}
func (a *agent) currentWiFi() (string, *int, *int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, _ := exec.CommandContext(ctx, "wpa_cli", "-i", "wlan0", "status").Output()
	signal, _ := exec.CommandContext(ctx, "wpa_cli", "-i", "wlan0", "signal_poll").Output()
	ssid, dbm, quality := wifiSignal(string(status), string(signal))
	if ssid == "" {
		return "", nil, nil
	}
	return ssid, dbm, quality
}
func (a *agent) wifiStatus() map[string]any {
	_, err := os.Stat(a.path("/tmp/ap.log"))
	ap := err == nil
	addresses := []string{}
	if interfaces, e := net.Interfaces(); e == nil {
		for _, iface := range interfaces {
			if iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			ips, _ := iface.Addrs()
			for _, ip := range ips {
				if v, ok := ip.(*net.IPNet); ok && v.IP.To4() != nil {
					addresses = append(addresses, v.IP.String())
				}
			}
		}
	}
	a.network.mu.Lock()
	state, problem := a.network.State, a.network.Error
	a.network.mu.Unlock()
	if state == "" {
		state = "idle"
	}
	a.portal.mu.Lock()
	portalHTTP, portalDNS, portalError := a.portal.HTTP, a.portal.DNS, a.portal.Error
	a.portal.mu.Unlock()
	ssid, dbm, quality := a.currentWiFi()
	return map[string]any{"ssid": ssid, "signal_dbm": dbm, "signal_percent": quality, "portal_http": portalHTTP, "portal_dns": portalDNS, "portal_error": portalError, "ap_mode": ap, "addresses": addresses, "state": state, "error": problem, "ap_default_url": "http://192.168.5.1:" + strconv.Itoa(a.cfg.Port) + "/"}
}
func (a *agent) setupWiFi(ctx context.Context, raw json.RawMessage) (any, error) {
	if !a.controlAllowed() {
		return nil, errors.New("Wi-Fi control requires pinned identity and allow_control")
	}
	ssid, password, err := validateWiFi(raw)
	if err != nil {
		return nil, err
	}
	script := a.path("/usr/bin/wifi_start.sh")
	source, err := readLimited(script, 128<<10)
	if err != nil {
		return nil, errors.New("native wifi_start.sh is unavailable")
	}
	// Recognize the inspected native adapter rather than invoke unknown firmware commands.
	if !strings.Contains(string(source), "select_master_ssid") || !strings.Contains(string(source), "get_master_network") {
		return nil, errors.New("unsupported native Wi-Fi adapter")
	}
	a.network.mu.Lock()
	if a.network.State == "connecting" {
		a.network.mu.Unlock()
		return nil, errors.New("Wi-Fi setup is already running")
	}
	a.network.State = "connecting"
	a.network.Error = ""
	a.network.mu.Unlock()
	go a.runWiFi(script, ssid, password)
	return map[string]any{"accepted": true, "state": "connecting", "ap_will_disconnect": true}, nil
}
func (a *agent) runWiFi(script, ssid, password string) {
	// Give the browser time to receive acceptance before the AP is shut down.
	time.Sleep(2 * time.Second)
	finish := func(state, problem string) {
		a.network.mu.Lock()
		a.network.State = state
		a.network.Error = problem
		a.network.mu.Unlock()
	}
	if _, err := a.identity(); err != nil {
		finish("failed", "robot identity check failed")
		return
	}
	path := a.path("/data/config/dmio/wifi.conf")
	previous, readErr := readLimited(path, 65536)
	if readErr != nil && !os.IsNotExist(readErr) {
		finish("failed", "cannot read existing Wi-Fi configuration")
		return
	}
	if readErr == nil {
		backup := a.path("/data/dreame-local/wifi.conf.before-agent")
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			if atomicPrivate(backup, previous) != nil {
				finish("failed", "cannot back up Wi-Fi configuration")
				return
			}
		}
	}
	if err := atomicPrivate(path, wifiConfig(ssid, password)); err != nil {
		finish("failed", "cannot save native Wi-Fi configuration")
		return
	}
	// No credentials in argv, agent configuration, responses or agent logs.
	task, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	cmd := exec.CommandContext(task, "/bin/sh", script, "SELECT_MASTER")
	cmd.Env = append(os.Environ(), "WIFI_CONF_FILE="+path)
	cmd.Stdout = nil
	cmd.Stderr = nil
	err := cmd.Run()
	cancel()
	connected := false
	if err == nil {
		check, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, e := exec.CommandContext(check, "wpa_cli", "-i", "wlan0", "status").Output()
		cancel()
		fields := map[string]string{}
		for _, line := range strings.Split(string(output), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok {
				fields[k] = v
			}
		}
		connected = e == nil && fields["wpa_state"] == "COMPLETED" && fields["ip_address"] != "" && fields["ssid"] == ssid
	}
	if connected {
		finish("connected", "")
		return
	}
	if readErr == nil {
		_ = atomicPrivate(path, previous)
	} else {
		_ = os.Remove(path)
	}
	recoverCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	recovery := exec.CommandContext(recoverCtx, "/bin/sh", script, "SET_AP_MODE")
	recovery.Env = append(os.Environ(), "WIFI_CONF_FILE="+path)
	recoveryErr := recovery.Run()
	cancel()
	if recoveryErr != nil {
		finish("failed", "connection failed; AP recovery also failed; use ADB")
	} else {
		finish("failed", "connection failed; previous configuration restored and AP restart requested")
	}
}
