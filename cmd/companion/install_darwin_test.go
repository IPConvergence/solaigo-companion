//go:build darwin

package main

import (
	"strings"
	"testing"
)

func TestPlistLogsSomewhereThatSurvivesARestart(t *testing.T) {
	plist := plistContent("/Users/x/Library/Application Support/solaigo-companion/bin/solaigo-companion",
		"/Users/x/Library/Logs/solaigo-companion.log")
	if strings.Contains(plist, "/tmp/") {
		t.Errorf("logs still go to /tmp, which a restart empties:\n%s", plist)
	}
	if !strings.Contains(plist, "<string>/Users/x/Library/Logs/solaigo-companion.log</string>") {
		t.Errorf("log path missing:\n%s", plist)
	}
	if !strings.Contains(plist, "<key>KeepAlive</key>") {
		t.Errorf("a crashed Companion should come back")
	}
}
