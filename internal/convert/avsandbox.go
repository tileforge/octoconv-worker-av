package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Measured read-only set for ffmpeg and ffprobe. The job directory is the
// only writable path and is granted per attempt.
var avReadOnly = []string{
	"/lib",
	"/etc/ld.so.cache",
	"/dev/urandom",
	"/proc/self/status",
	"/proc/sys/vm/overcommit_memory",
	"/sys/devices/system/cpu/online",
	"/sys/devices/system/cpu/possible",
}

var avEngines = []string{"ffmpeg", "ffprobe"}

const (
	avSandboxDisabledEnv   = "AV_SANDBOX_DISABLED"
	avPreflightOutsideBase = "/var/tmp"
)

func (c AVConverter) WithSandbox(helper []string) AVConverter {
	c.sandbox = newEngineSandbox(helper)
	return c
}

func (c AVConverter) jobExec(workDir string) engineExec {
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		workDir = resolved
	}
	return c.sandbox.forJobRW(workDir, nil, avReadOnly, avEngines).withEnv(engineEnv(""))
}

// AVProof is what AVPreflight observed inside the job directory.
type AVProof struct {
	OutputBytes int
}

func AVPreflight(ctx context.Context, c AVConverter) (AVProof, error) {
	if !c.sandbox.confines() {
		return AVProof{}, nil
	}
	w, cleanup, err := newPreflightWorkspaceOutside("av", avPreflightOutsideBase)
	if err != nil {
		return AVProof{}, err
	}
	defer cleanup()
	grants := append(append([]string{}, avReadOnly...), avEnginePaths()...)
	if err := w.requireOutsideGrants(grants); err != nil {
		return AVProof{}, err
	}

	srcInside := filepath.Join(w.jobDir, "in.mov")
	c.workDir = w.jobDir
	if _, err := c.exec(ctx, "ffmpeg", avPreflightSourceArgs(srcInside)...); err != nil {
		return AVProof{}, fmt.Errorf("generate preflight source: %w", err)
	}
	outInside := filepath.Join(w.jobDir, "out.mp4")
	if err := c.Convert(ctx, srcInside, outInside, nil); err != nil {
		return AVProof{}, fmt.Errorf("the engine cannot convert under the sandbox: %w", err)
	}
	out, err := os.ReadFile(outInside)
	if err != nil {
		return AVProof{}, fmt.Errorf("the engine cannot convert under the sandbox: read output: %w", err)
	}
	if len(out) < 8 || string(out[4:8]) != "ftyp" {
		return AVProof{}, fmt.Errorf("the engine cannot convert under the sandbox: output is not an mp4")
	}

	src, err := os.ReadFile(srcInside)
	if err != nil {
		return AVProof{}, fmt.Errorf("read preflight source: %w", err)
	}
	outside := filepath.Join(w.outsideDir, "in.mov")
	if err := os.WriteFile(outside, src, 0o600); err != nil {
		return AVProof{}, err
	}
	outOutside := filepath.Join(w.jobDir, "neg.mp4")
	convErr := c.Convert(ctx, outside, outOutside, nil)
	if err := deniedOutside(convErr, outOutside); err != nil {
		return AVProof{}, fmt.Errorf("the engine read a file outside the job directory: %w", err)
	}
	return AVProof{OutputBytes: len(out)}, nil
}

func avPreflightSourceArgs(outPath string) []string {
	return []string{
		"-y", "-nostdin", "-protocol_whitelist", "file,crypto",
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=10:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"file:" + outPath,
	}
}

func deniedOutside(convErr error, paths ...string) error {
	if convErr == nil {
		return fmt.Errorf("engine accepted the outside file")
	}
	if errors.Is(convErr, ErrEngineSandboxUnavailable) {
		return fmt.Errorf("sandbox setup failed: %w", convErr)
	}
	if strings.Contains(convErr.Error(), "locate engine ") {
		return fmt.Errorf("engine binary missing: %w", convErr)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("engine wrote %s", path)
		}
	}
	return nil
}

func avEnginePaths() []string {
	var paths []string
	for _, name := range avEngines {
		if p, err := exec.LookPath(name); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}
