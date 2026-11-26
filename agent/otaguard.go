// Copyright 2026 Dreame Local contributors. MIT license.
// Firmware-update switch. The firmware's OTA download script (ota_dw_fw.sh) first
// renames the boot hook (/data/test/ava.sh -> ava.sh_ok, "close development mode")
// and then downloads and installs, which drops the agent, every module and the
// region guard. When updates are switched off the agent bind-mounts a harmless
// stub over both OTA download scripts, so a triggered update does nothing: the
// hook is left in place and nothing is downloaded or installed. The block lives in
// /data (which the firmware's own update does not touch) and is re-applied at every
// startup, because the agent starts from the boot hook.
package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"syscall"
)

const (
	firmwareBlockFlag = "/data/dreame-local/firmware-updates-blocked"
	otaStubScript     = "/data/dreame-local/ota-blocked.sh"
)

// otaScripts are the firmware download scripts AvaExec runs for a full and an
// incremental update; a stub over each disarms both.
var otaScripts = []string{"/ava/script/ota_dw_fw.sh", "/ava/script/ota_dw_fw_xdelta.sh"}

const otaStubBody = "#!/bin/sh\n" +
	"# Installed by dreame-local-agent: firmware updates are switched off.\n" +
	"# Doing nothing here keeps the boot hook in place and downloads nothing.\n" +
	"echo \"$(date): firmware update blocked by dreame-local-agent\" >> /tmp/dreame-local-agent.log\n" +
	"exit 0\n"

// Mount primitives are variables so tests can replace them (binding a file needs root).
var (
	bindMount   = func(source, target string) error { return syscall.Mount(source, target, "", syscall.MS_BIND, "") }
	bindUnmount = func(target string) error { return syscall.Unmount(target, 0) }
	mountedOver = func(target string) bool {
		b, err := os.ReadFile("/proc/self/mountinfo")
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(b), "\n") {
			// mountinfo field 5 (index 4) is the mount point.
			if f := strings.Fields(line); len(f) >= 5 && f[4] == target {
				return true
			}
		}
		return false
	}
)

func (a *agent) firmwareBlocked() bool {
	_, err := os.Stat(a.path(firmwareBlockFlag))
	return err == nil
}

// armOTAStub writes the stub and bind-mounts it over any OTA script not already covered.
func (a *agent) armOTAStub() error {
	stub := a.path(otaStubScript)
	if err := os.WriteFile(stub, []byte(otaStubBody), 0700); err != nil {
		return errors.New("cannot write the update stub")
	}
	for _, s := range otaScripts {
		target := a.path(s)
		if _, err := os.Stat(target); err != nil {
			continue // script absent on this firmware
		}
		if mountedOver(target) {
			continue
		}
		if err := bindMount(stub, target); err != nil {
			return errors.New("cannot disarm " + s)
		}
	}
	return nil
}

// disarmOTAStub removes the stub mount from every OTA script (loop guards against a
// stack left by an older build).
func (a *agent) disarmOTAStub() {
	for _, s := range otaScripts {
		target := a.path(s)
		for i := 0; i < 4 && mountedOver(target); i++ {
			if bindUnmount(target) != nil {
				break
			}
		}
	}
}

func (a *agent) firmwareStatus() map[string]any {
	blocked := a.firmwareBlocked()
	armed := false
	for _, s := range otaScripts {
		if mountedOver(a.path(s)) {
			armed = true
			break
		}
	}
	return map[string]any{"enabled": !blocked, "armed": armed}
}

// setFirmwareUpdates turns the firmware updater on (enabled) or off.
func (a *agent) setFirmwareUpdates(enabled bool) (any, error) {
	if enabled {
		a.disarmOTAStub()
		if err := os.Remove(a.path(firmwareBlockFlag)); err != nil && !os.IsNotExist(err) {
			return nil, errors.New("cannot clear the update block")
		}
		return a.firmwareStatus(), nil
	}
	if err := atomicPrivate(a.path(firmwareBlockFlag), []byte("blocked\n")); err != nil {
		return nil, errors.New("cannot set the update block")
	}
	if err := a.armOTAStub(); err != nil {
		return nil, err
	}
	return a.firmwareStatus(), nil
}

// applyFirmwareBlock re-applies the block at startup when the flag is set; never fatal.
func (a *agent) applyFirmwareBlock() {
	if a.firmwareBlocked() {
		if err := a.armOTAStub(); err != nil {
			log.Print("firmware update block: ", err)
		}
	}
}

// firmwareRoutes handles GET/POST /api/v1/system/firmware; false when it is another path.
func (a *agent) firmwareRoutes(w http.ResponseWriter, r *http.Request, send func(int, any)) bool {
	if r.URL.Path != "/api/v1/system/firmware" {
		return false
	}
	if r.Method == "GET" {
		send(200, map[string]any{"result": a.firmwareStatus()})
		return true
	}
	if r.Method != "POST" {
		send(405, map[string]string{"error": "GET or POST required"})
		return true
	}
	if !a.controlAllowed() {
		send(403, map[string]string{"error": "changing firmware updates requires pinned identity and allow_control"})
		return true
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || req.Enabled == nil {
		send(400, map[string]string{"error": "enabled (true/false) required"})
		return true
	}
	v, err := a.setFirmwareUpdates(*req.Enabled)
	if err != nil {
		send(502, rpcErrorResponse(err))
		return true
	}
	send(200, map[string]any{"result": v})
	return true
}
