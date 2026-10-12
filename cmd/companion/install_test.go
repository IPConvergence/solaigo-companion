package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCopyExecutableWritesARunnableCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "downloaded")
	if err := os.WriteFile(src, []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "installed", "solaigo-companion")

	if err := copyExecutable(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, []byte("binary")) {
		t.Fatalf("copy = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(dst)
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("installed copy is not executable: %v", info.Mode())
		}
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Errorf("the temporary file was left behind")
	}
	// The download is untouched: deleting it later must not matter, and it is not ours.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the source was moved: %v", err)
	}
}

func TestCopyExecutableOntoItselfIsANoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "solaigo-companion")
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable(path, path); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "binary" {
		t.Errorf("running install from the installed copy changed it: %q", got)
	}
}

func TestPairPointsAtInstallNotAtAFuturePhase(t *testing.T) {
	// The message used to say "today, nothing stays running" while install existed.
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "nothing stays running") {
		t.Error("pair still tells people nothing stays running")
	}
}
