package main

import "testing"

// A placeholder test so `go test ./...` does something, and so CI's test step has
// shape even before the pair/transport/detect/proxy packages exist. Replace this
// as the real packages arrive.
func TestVersionStringNotEmpty(t *testing.T) {
	if version == "" {
		t.Fatal("version must not be empty; -ldflags sets it in release builds")
	}
}
