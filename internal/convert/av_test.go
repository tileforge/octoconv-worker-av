package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPairsMatchManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schema", "manifest-av-convert.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Capabilities []struct {
			ID     string `json:"id"`
			Legacy *struct {
				Operation string `json:"operation"`
				Engine    string `json:"engine"`
			} `json:"legacy"`
			Formats []Pair `json:"formats"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Capabilities) != 1 || doc.Capabilities[0].ID != "octoconv.convert.av" {
		t.Fatalf("capability %+v", doc.Capabilities)
	}
	legacy := doc.Capabilities[0].Legacy
	if legacy == nil || legacy.Engine != EngineAV || legacy.Operation != OperationConvert {
		t.Fatalf("legacy %+v", legacy)
	}
	want := (AVConverter{}).Pairs()
	got := doc.Capabilities[0].Formats
	if len(got) != 35 || len(got) != len(want) {
		t.Fatalf("formats %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pair %d = %+v, want %+v", i, got[i], want[i])
		}
		conv, ok := Default.LookupOp(OperationConvert, got[i].From, got[i].To)
		if !ok {
			t.Fatalf("missing %s to %s", got[i].From, got[i].To)
		}
		if _, isAV := conv.(AVConverter); !isAV {
			t.Fatalf("%s to %s converter %T", got[i].From, got[i].To, conv)
		}
	}
}

func TestLimits(t *testing.T) {
	if DefaultEngineTimeout != 753*time.Second {
		t.Fatalf("timeout %s", DefaultEngineTimeout)
	}
	if MaxFileBytes != 2<<30 {
		t.Fatalf("max file %d", MaxFileBytes)
	}
}

func TestProbeArgsUseFileProtocol(t *testing.T) {
	for _, args := range [][]string{
		ffprobeDurationArgs("/work/in.mp4"),
		ffprobeStreamArgs("/work/in.mp4"),
		ffprobeAudioCodecArgs("/work/in.mp4"),
		avInputArgs("/work/in.mp4"),
	} {
		if !containsArg(args, "-protocol_whitelist", "file,crypto") {
			t.Fatalf("args %v", args)
		}
		last := args[len(args)-1]
		if last != "file:/work/in.mp4" && !strings.HasPrefix(last, "file:") && args[len(args)-2] != "file:/work/in.mp4" {
			found := false
			for _, arg := range args {
				if arg == "file:/work/in.mp4" {
					found = true
				}
			}
			if !found {
				t.Fatalf("args %v", args)
			}
		}
	}
}

func TestParameterSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schema", "manifest-av-convert.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Capabilities []struct {
			Schema map[string]any `json:"parameter_schema"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	schema := doc.Capabilities[0].Schema
	if schema["type"] != "object" || schema["additionalProperties"] != false || len(schema) != 3 {
		t.Fatalf("schema %+v", schema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || len(props) != 3 {
		t.Fatalf("properties %+v", schema["properties"])
	}
	timecode, _ := props["timecode"].(map[string]any)
	if timecode["type"] != "number" || timecode["minimum"] != float64(0) || len(timecode) != 2 {
		t.Fatalf("timecode %+v", timecode)
	}
	height, _ := props["resolution_height"].(map[string]any)
	enum, _ := height["enum"].([]any)
	if height["type"] != "integer" || len(height) != 2 || len(enum) != 3 || enum[0] != float64(480) || enum[1] != float64(720) || enum[2] != float64(1080) {
		t.Fatalf("resolution_height %+v", height)
	}
	codec, _ := props["codec"].(map[string]any)
	codecs, _ := codec["enum"].([]any)
	if codec["type"] != "string" || len(codec) != 2 || len(codecs) != 2 || codecs[0] != "h264" || codecs[1] != "hevc" {
		t.Fatalf("codec %+v", codec)
	}
}

func TestConvertRejectsIllegalOptionsBeforeExec(t *testing.T) {
	called := false
	c := AVConverter{run: func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}}
	for _, opts := range []map[string]any{
		{"timecode": 1.0},
		{"timecode": -1.0},
		{"timecode": "0"},
		{"resolution_height": 144},
		{"resolution_height": 720.5},
		{"resolution_height": "720"},
		{"codec": "vp9"},
		{"codec": ""},
		{"page_size": "a4"},
	} {
		err := c.Convert(context.Background(), "in.mp4", filepath.Join(t.TempDir(), "out.mp4"), opts)
		if err == nil || !strings.Contains(err.Error(), "unsupported option") {
			t.Fatalf("opts %v err %v", opts, err)
		}
	}
	err := c.Convert(context.Background(), "in.mp4", filepath.Join(t.TempDir(), "out.jpg"), map[string]any{"resolution_height": 720})
	if err == nil || !strings.Contains(err.Error(), "unsupported option") {
		t.Fatal(err)
	}
	err = c.Convert(context.Background(), "in.mp4", filepath.Join(t.TempDir(), "out.webm"), map[string]any{"codec": "hevc"})
	if err == nil || !strings.Contains(err.Error(), "unsupported option") {
		t.Fatal(err)
	}
	err = c.Convert(context.Background(), "in.mp4", filepath.Join(t.TempDir(), "out.mp3"), map[string]any{"timecode": 0})
	if err == nil || !strings.Contains(err.Error(), "unsupported option") {
		t.Fatal(err)
	}
	if called {
		t.Fatal("exec ran")
	}
}

func TestConvertRejectsUnknownTargetBeforeExec(t *testing.T) {
	called := false
	c := AVConverter{run: func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}}
	err := c.Convert(context.Background(), "in.mp4", filepath.Join(t.TempDir(), "out.xyz"), nil)
	if err == nil || called {
		t.Fatalf("err %v called %v", err, called)
	}
}

func TestConvertCopiesH264AndReencodesVP9(t *testing.T) {
	dir := t.TempDir()
	var ffmpeg []string
	c := AVConverter{
		threads: 2,
		run:     scripted(t, "2.0", "h264", "aac", 320, 240, &ffmpeg),
	}
	out := filepath.Join(dir, "out.mp4")
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mov"), out, nil); err != nil {
		t.Fatal(err)
	}
	if !containsArg(ffmpeg, "-c", "copy") || containsArg(ffmpeg, "-c:v", "libx264") {
		t.Fatalf("copy args %v", ffmpeg)
	}
	if !containsArg(ffmpeg, "-map", "0:1") {
		t.Fatalf("map %v", ffmpeg)
	}

	ffmpeg = nil
	c.run = scripted(t, "2.0", "vp9", "opus", 320, 240, &ffmpeg)
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mov"), out, nil); err != nil {
		t.Fatal(err)
	}
	if containsArg(ffmpeg, "-c", "copy") || !containsArg(ffmpeg, "-c:v", "libx264") || !containsArg(ffmpeg, "-crf", "23") {
		t.Fatalf("reencode args %v", ffmpeg)
	}
	if !containsArg(ffmpeg, "-threads", "2") {
		t.Fatalf("threads %v", ffmpeg)
	}
}

func TestConvertRejectsOversizedReencodeBeforeFFmpeg(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := 0
	c := AVConverter{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "ffmpeg" {
			ffmpeg++
			return nil, fmt.Errorf("ffmpeg")
		}
		return fakeProbe(args, "2.0", "vp9", "opus", 3840, 2160)
	}}
	err := c.Convert(context.Background(), filepath.Join(dir, "in.mov"), filepath.Join(dir, "out.mp4"), nil)
	if err == nil || ffmpeg != 0 || !strings.Contains(err.Error(), "re-encode source resolution") {
		t.Fatalf("ffmpeg %d err %v", ffmpeg, err)
	}
}

func TestConvertKeepsALargeStreamCopy(t *testing.T) {
	dir := t.TempDir()
	var ffmpeg []string
	c := AVConverter{run: scripted(t, "2.0", "h264", "aac", 3840, 2160, &ffmpeg)}
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mov"), filepath.Join(dir, "out.mp4"), nil); err != nil {
		t.Fatal(err)
	}
	if !containsArg(ffmpeg, "-c", "copy") {
		t.Fatalf("args %v", ffmpeg)
	}
}

func TestConvertExtractsAudioAndThumb(t *testing.T) {
	dir := t.TempDir()
	var ffmpeg []string
	c := AVConverter{run: scripted(t, "2.0", "h264", "aac", 320, 240, &ffmpeg)}
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mp4"), filepath.Join(dir, "out.m4a"), nil); err != nil {
		t.Fatal(err)
	}
	if !containsArg(ffmpeg, "-c:a", "copy") || !hasFlag(ffmpeg, "-vn") || !containsArg(ffmpeg, "-map", "0:a:0") {
		t.Fatalf("m4a %v", ffmpeg)
	}

	ffmpeg = nil
	c.run = scripted(t, "2.0", "h264", "aac", 320, 240, &ffmpeg)
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mp4"), filepath.Join(dir, "out.mp3"), nil); err != nil {
		t.Fatal(err)
	}
	if !containsArg(ffmpeg, "-c:a", "libmp3lame") {
		t.Fatalf("mp3 %v", ffmpeg)
	}

	ffmpeg = nil
	c.run = scripted(t, "0.4", "h264", "aac", 320, 240, &ffmpeg)
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mp4"), filepath.Join(dir, "out.jpg"), nil); err != nil {
		t.Fatal(err)
	}
	ssAt := -1
	inAt := -1
	for i, arg := range ffmpeg {
		if arg == "-ss" {
			ssAt = i
		}
		if arg == "-i" {
			inAt = i
		}
	}
	if ssAt < 0 || inAt < 0 || ssAt > inAt || ffmpeg[ssAt+1] != "0.2" {
		t.Fatalf("seek %v", ffmpeg)
	}
	if !containsArg(ffmpeg, "-c:v", "mjpeg") {
		t.Fatalf("jpg %v", ffmpeg)
	}
}

func TestConvertHonorsTimecodeResolutionAndCodec(t *testing.T) {
	dir := t.TempDir()
	var ffmpeg []string
	c := AVConverter{
		threads: 2,
		run:     scripted(t, "2.0", "h264", "aac", 320, 240, &ffmpeg),
	}
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mp4"), filepath.Join(dir, "out.jpg"), map[string]any{"timecode": 0.0}); err != nil {
		t.Fatal(err)
	}
	if !containsArg(ffmpeg, "-ss", "0") {
		t.Fatalf("seek %v", ffmpeg)
	}

	ffmpeg = nil
	c.run = scripted(t, "2.0", "h264", "aac", 320, 240, &ffmpeg)
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mov"), filepath.Join(dir, "out.mp4"), map[string]any{"resolution_height": float64(720), "codec": "h264"}); err != nil {
		t.Fatal(err)
	}
	if containsArg(ffmpeg, "-c", "copy") || !containsArg(ffmpeg, "-vf", "scale=-2:720") || !containsArg(ffmpeg, "-c:v", "libx264") || !containsArg(ffmpeg, "-crf", "23") {
		t.Fatalf("scale args %v", ffmpeg)
	}

	ffmpeg = nil
	c.run = scripted(t, "2.0", "h264", "aac", 320, 240, &ffmpeg)
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mov"), filepath.Join(dir, "out.mp4"), map[string]any{"codec": "hevc"}); err != nil {
		t.Fatal(err)
	}
	if containsArg(ffmpeg, "-c", "copy") || !containsArg(ffmpeg, "-c:v", "libx265") || !containsArg(ffmpeg, "-crf", "28") {
		t.Fatalf("hevc args %v", ffmpeg)
	}

	ffmpeg = nil
	c.run = scripted(t, "2.0", "h264", "aac", 320, 240, &ffmpeg)
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mov"), filepath.Join(dir, "out.mp4"), map[string]any{"codec": "h264"}); err != nil {
		t.Fatal(err)
	}
	if !containsArg(ffmpeg, "-c", "copy") {
		t.Fatalf("explicit h264 copy %v", ffmpeg)
	}

	ffmpeg = nil
	c.run = scripted(t, "2.0", "vp9", "opus", 320, 240, &ffmpeg)
	if err := c.Convert(context.Background(), filepath.Join(dir, "in.mp4"), filepath.Join(dir, "out.webm"), map[string]any{"resolution_height": float64(480)}); err != nil {
		t.Fatal(err)
	}
	if !containsArg(ffmpeg, "-vf", "scale=-2:480") || !containsArg(ffmpeg, "-c:v", "libvpx-vp9") {
		t.Fatalf("webm args %v", ffmpeg)
	}

	probes := 0
	ffmpegCount := 0
	c.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			probes++
			return fakeProbe(args, "2.0", "h264", "aac", 320, 240)
		}
		ffmpegCount++
		return nil, fmt.Errorf("ffmpeg")
	}
	err := c.Convert(context.Background(), filepath.Join(dir, "in.mp4"), filepath.Join(dir, "out.png"), map[string]any{"timecode": 2.0})
	if err == nil || probes == 0 || ffmpegCount != 0 || !strings.Contains(err.Error(), "timecode exceeds source duration") {
		t.Fatalf("probes %d ffmpeg %d err %v", probes, ffmpegCount, err)
	}
}

func TestConvertRejectsALongSourceBeforeFFmpeg(t *testing.T) {
	ffmpeg := 0
	c := AVConverter{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "ffmpeg" {
			ffmpeg++
		}
		return fakeProbe(args, "20000", "h264", "aac", 320, 240)
	}}
	err := c.Convert(context.Background(), "in.mp4", filepath.Join(t.TempDir(), "out.mp3"), nil)
	if err == nil || ffmpeg != 0 || !strings.Contains(err.Error(), "exceeds ceiling") {
		t.Fatalf("ffmpeg %d err %v", ffmpeg, err)
	}
}

func TestConvertRejectsAnEmptyOutput(t *testing.T) {
	c := AVConverter{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "ffmpeg" {
			return nil, nil
		}
		return fakeProbe(args, "2.0", "h264", "aac", 320, 240)
	}}
	err := c.Convert(context.Background(), "in.mp4", filepath.Join(t.TempDir(), "out.mp3"), nil)
	if err == nil || !strings.Contains(err.Error(), "output missing or empty") {
		t.Fatal(err)
	}
}

func TestGoSourcesDoNotNameLandlock(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(bytes.ToLower(body), []byte("landlock")) {
			t.Fatalf("%s names landlock", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMIMETypes(t *testing.T) {
	want := map[string]string{
		"mp4":  "video/mp4",
		"webm": "video/webm",
		"mp3":  "audio/mpeg",
		"wav":  "audio/wav",
		"m4a":  "audio/mp4",
		"jpg":  "image/jpeg",
		"png":  "image/png",
		"webp": "image/webp",
	}
	for format, mime := range want {
		if MIMEType(format) != mime {
			t.Fatalf("%s %s", format, MIMEType(format))
		}
	}
}

func scripted(t *testing.T, duration, video, audio string, width, height int, ffmpeg *[]string) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			return fakeProbe(args, duration, video, audio, width, height)
		}
		if name != "ffmpeg" {
			return nil, fmt.Errorf("unexpected %s", name)
		}
		*ffmpeg = append([]string(nil), args...)
		last := strings.TrimPrefix(args[len(args)-1], "file:")
		body := []byte("out")
		switch filepath.Ext(last) {
		case ".jpg":
			body = append([]byte{0xff, 0xd8, 0xff}, bytes.Repeat([]byte{0}, 16)...)
		case ".png":
			body = append([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{0}, 8)...)
		case ".webp":
			body = append([]byte("RIFF"), 0, 0, 0, 0)
			body = append(body, []byte("WEBP")...)
			body = append(body, bytes.Repeat([]byte{0}, 4)...)
		}
		if err := os.WriteFile(last, body, 0o600); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

func fakeProbe(args []string, duration, video, audio string, width, height int) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "format=duration"):
		return []byte(duration + "\n"), nil
	case strings.Contains(joined, "select_streams v"):
		raw, err := json.Marshal(map[string]any{
			"streams": []map[string]any{
				{
					"index": 0, "codec_name": "mjpeg", "width": 64, "height": 64,
					"disposition": map[string]int{"attached_pic": 1},
				},
				{
					"index": 1, "codec_name": video, "width": width, "height": height,
					"disposition": map[string]int{"attached_pic": 0},
				},
			},
		})
		return raw, err
	case strings.Contains(joined, "select_streams a:0"):
		return []byte(audio + "\n"), nil
	default:
		return nil, fmt.Errorf("ffprobe %v", args)
	}
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

func containsArg(args []string, key, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
