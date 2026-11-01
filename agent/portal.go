package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const portalIP = "192.168.5.1"

type portalState struct {
	mu    sync.Mutex
	HTTP  bool
	DNS   bool
	Error string
}

func portalDNSConfig(source []byte) ([]byte, error) {
	s := string(source)
	if !strings.Contains(s, "interface=") || !strings.Contains(s, "dhcp-range=") {
		return nil, errors.New("unrecognized native AP DNS/DHCP configuration")
	}
	// Keep the native DHCP options; only AP DNS answers change.
	s += "\n# Dreame Local captive portal (AP only)\naddress=/#/" + portalIP + "\n"
	return []byte(s), nil
}
func portalRedirect(port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Connection", "close")
		http.Redirect(w, r, "http://"+portalIP+":"+strconv.Itoa(port)+"/?setup=wifi", http.StatusFound)
	})
}
func hasPortalAddress() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range interfaces {
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			if ip, ok := address.(*net.IPNet); ok && ip.IP.String() == portalIP {
				return true
			}
		}
	}
	return false
}
func (a *agent) portalActive() bool {
	_, err := os.Stat(a.path("/tmp/ap.log"))
	return err == nil && hasPortalAddress()
}
func (a *agent) startPortalDNS() error {
	source, err := readLimited(a.path("/tmp/dnsmasq.conf"), 65536)
	if err != nil {
		return errors.New("native AP DNS configuration is unavailable")
	}
	data, err := portalDNSConfig(source)
	if err != nil {
		return err
	}
	path := a.path("/tmp/dreame-local-portal-dnsmasq.conf")
	if err = atomicPrivate(path, data); err != nil {
		return err
	}
	task, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = exec.CommandContext(task, "dnsmasq", "--test", "-C", path).Run(); err != nil {
		return errors.New("AP portal DNS configuration was rejected")
	}
	pidBytes, err := readLimited(a.path("/tmp/dnsmasq.pid"), 64)
	if err != nil {
		return errors.New("native AP DNS PID is unavailable")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 1 {
		return errors.New("invalid AP DNS PID")
	}
	exe, err := os.Readlink(a.path("/proc/" + strconv.Itoa(pid) + "/exe"))
	if err != nil || filepath.Base(exe) != "dnsmasq" {
		return errors.New("AP DNS PID identity mismatch")
	}
	if !a.portalActive() {
		return errors.New("AP mode ended during portal setup")
	}
	if err = syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 20; i++ {
		if syscall.Kill(pid, 0) != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err = exec.CommandContext(task, "dnsmasq", "-C", path, "-x", "/tmp/dnsmasq.pid").Run(); err != nil {
		// Restore native DNS/DHCP if portal activation fails.
		recovery, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = exec.CommandContext(recovery, "dnsmasq", "-C", "/tmp/dnsmasq.conf", "-x", "/tmp/dnsmasq.pid").Run()
		return errors.New("portal DNS failed; native DNS restart requested")
	}
	return nil
}
func (a *agent) restorePortalDNS() {
	pidBytes, err := readLimited(a.path("/tmp/dnsmasq.pid"), 64)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 1 {
		return
	}
	command, err := readLimited(a.path("/proc/"+strconv.Itoa(pid)+"/cmdline"), 65536)
	if err != nil || !strings.Contains(string(command), "/tmp/dreame-local-portal-dnsmasq.conf") {
		return
	}
	if !a.portalActive() {
		return
	}
	if syscall.Kill(pid, syscall.SIGTERM) != nil {
		return
	}
	for i := 0; i < 20; i++ {
		if syscall.Kill(pid, 0) != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	task, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(task, "dnsmasq", "-C", "/tmp/dnsmasq.conf", "-x", "/tmp/dnsmasq.pid").Run()
}

func (a *agent) runPortal(ctx context.Context) {
	var server *http.Server
	var nextDNSAttempt time.Time
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	stop := func() {
		nextDNSAttempt = time.Time{}
		if server != nil {
			_ = server.Close()
			server = nil
		}
		a.portal.mu.Lock()
		a.portal.HTTP = false
		a.portal.DNS = false
		a.portal.Error = ""
		a.portal.mu.Unlock()
	}
	defer func() { a.restorePortalDNS(); stop() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !a.portalActive() {
			stop()
			continue
		}
		if server == nil {
			listener, err := net.Listen("tcp4", portalIP+":80")
			if err != nil {
				a.portal.mu.Lock()
				a.portal.Error = "AP port 80 is unavailable"
				a.portal.mu.Unlock()
				continue
			}
			server = &http.Server{Handler: portalRedirect(a.cfg.Port), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16384}
			go func(s *http.Server, l net.Listener) { _ = s.Serve(l) }(server, listener)
			a.portal.mu.Lock()
			a.portal.HTTP = true
			a.portal.mu.Unlock()
		}
		a.portal.mu.Lock()
		dns := a.portal.DNS
		a.portal.mu.Unlock()
		if !dns && time.Now().After(nextDNSAttempt) {
			nextDNSAttempt = time.Now().Add(30 * time.Second)
			err := a.startPortalDNS()
			a.portal.mu.Lock()
			a.portal.DNS = err == nil
			if err != nil {
				a.portal.Error = err.Error()
			} else {
				a.portal.Error = ""
			}
			a.portal.mu.Unlock()
		}
	}
}
