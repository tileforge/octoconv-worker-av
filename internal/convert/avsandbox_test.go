package convert

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tileforge/octoconv-worker-av/internal/sandbox"
)

func TestAVGrantsMatchTheMeasuredSet(t *testing.T) {
	if strings.Join(avReadOnly, "\n") != strings.Join([]string{
		"/lib",
		"/etc/ld.so.cache",
		"/dev/urandom",
		"/proc/self/status",
		"/proc/sys/vm/overcommit_memory",
		"/sys/devices/system/cpu/online",
		"/sys/devices/system/cpu/possible",
	}, "\n") {
		t.Fatalf("read only %v", avReadOnly)
	}
	if strings.Join(avEngines, "\n") != "ffmpeg\nffprobe" {
		t.Fatalf("engines %v", avEngines)
	}
	if avPreflightOutsideBase == "/tmp" || avSandboxDisabledEnv != "AV_SANDBOX_DISABLED" {
		t.Fatal("outside fixture or disable env")
	}
}

func TestWithSandboxEmptyHelperIsUnconfined(t *testing.T) {
	c := AVConverter{}.WithSandbox(nil)
	if c.sandbox.confines() {
		t.Fatal("confined")
	}
	proof, err := AVPreflight(t.Context(), c)
	if err != nil || proof.OutputBytes != 0 {
		t.Fatalf("proof %+v err %v", proof, err)
	}
}

func TestForJobGrantsTheJobDirectoryAndEngines(t *testing.T) {
	dir := t.TempDir()
	for _, name := range avEngines {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	job := filepath.Join(dir, "job")
	c := AVConverter{}.WithSandbox([]string{"/usr/local/bin/av-worker", sandbox.ExecMode})
	spec := strings.Join(c.jobExec(job).spec, "\n")
	for _, want := range []string{
		"rw:" + job,
		"ro:/lib",
		"ro:/dev/urandom",
		"ro:/proc/self/status",
		"ro:" + filepath.Join(dir, "ffmpeg"),
		"ro:" + filepath.Join(dir, "ffprobe"),
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("missing %s in %s", want, spec)
		}
	}
	for _, token := range strings.Split(spec, "\n") {
		if token == "rw:/" || token == "rw:/tmp" || strings.HasPrefix(token, "rw:/tmp/") {
			t.Fatalf("spec grants a shared writable path: %s", spec)
		}
	}
	var bare AVConverter
	if got := bare.jobExec(job).spec; len(got) != 0 {
		t.Fatalf("unconfined spec %q", got)
	}
}

func TestDeniedOutsideRejectsSetupFailureAndAWrittenFile(t *testing.T) {
	if err := deniedOutside(ErrEngineSandboxUnavailable); err == nil || !strings.Contains(err.Error(), "sandbox setup failed") {
		t.Fatal(err)
	}
	if err := deniedOutside(errors.New("locate engine ffmpeg: exec: not found")); err == nil || !strings.Contains(err.Error(), "engine binary missing") {
		t.Fatal(err)
	}
	dir := t.TempDir()
	written := filepath.Join(dir, "neg.mp4")
	if err := os.WriteFile(written, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deniedOutside(errors.New("permission denied"), written); err == nil || !strings.Contains(err.Error(), "engine wrote") {
		t.Fatal(err)
	}
}
