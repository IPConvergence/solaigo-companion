//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestUnitContentHasTheExecutablePath(t *testing.T) {
	exe := "/home/lemaimn/.local/bin/solaigo-companion"
	unit := unitContent(exe)
	if !strings.Contains(unit, "ExecStart="+exe+" daemon") {
		t.Errorf("unit is missing the executable path.\n--- unit ---\n%s", unit)
	}
	if !strings.Contains(unit, "Restart=on-failure") {
		t.Errorf("unit should restart on failure.\n--- unit ---\n%s", unit)
	}
	if !strings.Contains(unit, "[Install]\nWantedBy=default.target") {
		t.Errorf("unit should install into default.target so it runs on login")
	}
}

func TestUnitPathUsesXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	path, err := unitPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != "/custom/config/systemd/user/solaigo-companion.service" {
		t.Errorf("unitPath = %q, want /custom/config/systemd/user/…", path)
	}
}
