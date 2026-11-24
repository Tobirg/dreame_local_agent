package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirmwareSwitchBlocksAndRestores(t *testing.T) {
	a := testAgent(t)
	if err := os.MkdirAll(a.path("/data/dreame-local"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, s := range otaScripts {
		if err := os.MkdirAll(filepath.Dir(a.path(s)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(a.path(s), []byte("#!/bin/sh\n# real ota\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	mounted := map[string]bool{}
	mounts := 0
	orig1, orig2, orig3 := bindMount, bindUnmount, mountedOver
	t.Cleanup(func() { bindMount, bindUnmount, mountedOver = orig1, orig2, orig3 })
	bindMount = func(source, target string) error { mounts++; mounted[target] = true; return nil }
	bindUnmount = func(target string) error { delete(mounted, target); return nil }
	mountedOver = func(target string) bool { return mounted[target] }

	// Block: flag written, stub bind-mounted over both scripts.
	if _, err := a.setFirmwareUpdates(false); err != nil {
		t.Fatal(err)
	}
	if !a.firmwareBlocked() {
		t.Fatal("flag not set after blocking")
	}
	if _, err := os.Stat(a.path(otaStubScript)); err != nil {
		t.Fatal("stub not written")
	}
	if len(mounted) != len(otaScripts) || mounts != len(otaScripts) {
		t.Fatalf("want %d mounts, got %d (active %d)", len(otaScripts), mounts, len(mounted))
	}
	if st := a.firmwareStatus(); st["enabled"] != false || st["armed"] != true {
		t.Fatalf("blocked status wrong: %v", st)
	}

	// Re-applying at startup must not stack mounts.
	a.applyFirmwareBlock()
	if mounts != len(otaScripts) {
		t.Fatalf("startup re-armed already-mounted scripts: %d mounts", mounts)
	}

	// Unblock: flag cleared, both unmounted.
	if _, err := a.setFirmwareUpdates(true); err != nil {
		t.Fatal(err)
	}
	if a.firmwareBlocked() {
		t.Fatal("flag still set after enabling")
	}
	if len(mounted) != 0 {
		t.Fatalf("mounts left after enabling: %v", mounted)
	}
	if st := a.firmwareStatus(); st["enabled"] != true || st["armed"] != false {
		t.Fatalf("enabled status wrong: %v", st)
	}
}

func TestFirmwareSwitchSkipsMissingScript(t *testing.T) {
	a := testAgent(t)
	if err := os.MkdirAll(a.path("/data/dreame-local"), 0700); err != nil {
		t.Fatal(err)
	}
	// Only the first script exists on this firmware; the xdelta one is absent.
	if err := os.MkdirAll(filepath.Dir(a.path(otaScripts[0])), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.path(otaScripts[0]), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	mounted := map[string]bool{}
	orig1, orig2, orig3 := bindMount, bindUnmount, mountedOver
	t.Cleanup(func() { bindMount, bindUnmount, mountedOver = orig1, orig2, orig3 })
	bindMount = func(source, target string) error { mounted[target] = true; return nil }
	bindUnmount = func(target string) error { delete(mounted, target); return nil }
	mountedOver = func(target string) bool { return mounted[target] }

	if _, err := a.setFirmwareUpdates(false); err != nil {
		t.Fatal(err)
	}
	if len(mounted) != 1 {
		t.Fatalf("want 1 mount over the present script, got %d", len(mounted))
	}
}
