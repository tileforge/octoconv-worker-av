// Command av-worker runs one approved av capability over Worker Protocol v2.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tileforge/octoconv-worker-av/internal/convert"
	"github.com/tileforge/octoconv-worker-av/internal/sandbox"
)

const avSandboxDisabledEnv = "AV_SANDBOX_DISABLED"

func main() {
	if sandbox.RunExecModeIfRequested(os.Args) {
		return
	}
	if err := refuseLegacyStores(); err != nil {
		log.Fatalf("av protocol: %v", err)
	}
	if raw := os.Getenv("AV_THREADS"); raw != "" {
		n, err := strconv.Atoi(firstField(raw))
		if err != nil || n < 1 {
			log.Fatalf("av protocol: AV_THREADS must be a positive integer")
		}
		convert.SetAVThreads(n)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sandboxHelper, err := sandbox.ResolveHelper(avSandboxDisabledEnv)
	if err != nil {
		log.Fatalf("av sandbox: %v", err)
	}
	conv := convert.AVConverter{}.WithSandbox(sandboxHelper)
	convert.Default.Register(conv)
	if len(sandboxHelper) == 0 {
		log.Printf("%s=true: the engine can read every file in this container", avSandboxDisabledEnv)
	} else {
		log.Printf("av sandbox: landlock via %s", strings.Join(sandboxHelper, " "))
		if err := convert.StabilizeTempDir(); err != nil {
			log.Fatalf("av sandbox: %v", err)
		}
	}

	preflightCtx, cancelPreflight := context.WithTimeout(ctx, 60*time.Second)
	proof, err := convert.AVPreflight(preflightCtx, conv)
	cancelPreflight()
	if err != nil {
		log.Fatalf("av sandbox preflight: %v", err)
	}
	if len(sandboxHelper) > 0 {
		log.Printf("av sandbox proven: ffmpeg and ffprobe ran inside the job directory and were denied a file outside every grant (output %d bytes, no outside output)", proof.OutputBytes)
	}

	dial, err := loadProtocolDial()
	if err != nil {
		log.Fatalf("av protocol: %v", err)
	}
	log.Printf("av protocol worker starting")
	if err := runProtocol(ctx, dial); err != nil {
		log.Fatalf("av protocol: %v", err)
	}
}

func refuseLegacyStores() error {
	if os.Getenv("DATABASE_URL") != "" || os.Getenv("REDIS_ADDR") != "" {
		return errors.New("DATABASE_URL and REDIS_ADDR are not used")
	}
	return nil
}

func avFreeSlots() (int, error) {
	raw := os.Getenv("AV_WORKER_CONCURRENCY")
	if raw == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(firstField(raw))
	if err != nil || n != 1 {
		return 0, errors.New("AV_WORKER_CONCURRENCY must be 1")
	}
	return 1, nil
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(firstField(v)); err == nil {
			return d
		}
	}
	return def
}

func firstField(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			return s[:i]
		}
	}
	return s
}
