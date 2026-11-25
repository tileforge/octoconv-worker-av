package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAVRenderManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schema", "manifest-av-render.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Capabilities []struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
			Legacy  *struct {
				Operation string `json:"operation"`
				Engine    string `json:"engine"`
			} `json:"legacy"`
			ClientOperation string          `json:"client_operation"`
			Inputs          []manifestBound `json:"inputs"`
			Outputs         []manifestBound `json:"outputs"`
			Schema          json.RawMessage `json:"parameter_schema"`
			Formats         []Pair          `json:"formats"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Capabilities) != 1 {
		t.Fatalf("capabilities %d", len(doc.Capabilities))
	}
	cap := doc.Capabilities[0]
	if cap.ID != "octoconv.render.av" || cap.Version != 1 {
		t.Fatalf("capability %s version %d", cap.ID, cap.Version)
	}
	if cap.Legacy == nil || cap.Legacy.Operation != OperationRender || cap.Legacy.Engine != EngineAV {
		t.Fatalf("legacy %+v", cap.Legacy)
	}
	if cap.ClientOperation != OperationRender {
		t.Fatalf("client operation %s", cap.ClientOperation)
	}
	if len(cap.Inputs) != 1 || cap.Inputs[0] != (manifestBound{Role: "source", Min: 1, Max: 1}) {
		t.Fatalf("inputs %+v", cap.Inputs)
	}
	if len(cap.Outputs) != 1 || cap.Outputs[0] != (manifestBound{Role: "primary", Min: 1, Max: 1}) {
		t.Fatalf("outputs %+v", cap.Outputs)
	}
	if !bytes.Equal(bytes.TrimSpace(cap.Schema), []byte("{}")) {
		t.Fatalf("schema %s", cap.Schema)
	}
	want := (AVRenderConverter{}).Pairs()
	if len(cap.Formats) != len(want) {
		t.Fatalf("formats %d", len(cap.Formats))
	}
	for i := range want {
		if cap.Formats[i] != want[i] || want[i].To != "png" {
			t.Fatalf("pair %d = %+v, want %+v", i, cap.Formats[i], want[i])
		}
		conv, ok := Default.LookupOp(OperationRender, want[i].From, "png")
		if !ok {
			t.Fatalf("missing render %s", want[i].From)
		}
		if _, isRender := conv.(AVRenderConverter); !isRender {
			t.Fatalf("render converter %T", conv)
		}
	}
	conv, ok := Default.LookupOp(OperationConvert, "mp4", "png")
	if !ok {
		t.Fatal("missing convert mp4 to png")
	}
	if _, isAV := conv.(AVConverter); !isAV {
		t.Fatalf("convert converter %T", conv)
	}
	if MIMEType("png") != "image/png" {
		t.Fatalf("mime %s", MIMEType("png"))
	}
	defer func() {
		if recover() != nil {
			t.Fatal("register panicked")
		}
	}()
	Default.Register(AVRenderConverter{})
	Default.Register(AVConverter{})
}

type manifestBound struct {
	Role string `json:"role"`
	Min  int    `json:"min"`
	Max  int    `json:"max"`
}

func TestAVRenderFrame(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.mp4")
	for _, duration := range []struct {
		raw  string
		seek string
	}{
		{raw: "5", seek: "1"},
		{raw: "1.0", seek: "0.5"},
		{raw: "0.4", seek: "0.2"},
	} {
		outPath := filepath.Join(dir, "out-"+duration.seek+".png")
		probes, ffmpeg, args := 0, 0, []string(nil)
		c := AVRenderConverter{AVConverter: AVConverter{run: countedScript(t, duration.raw, 320, 240, &probes, &ffmpeg, &args)}}
		if err := c.Convert(context.Background(), inPath, outPath, nil); err != nil {
			t.Fatal(err)
		}
		assertRenderFrame(t, args, probes, ffmpeg, duration.seek, inPath, outPath, avRenderFilter(avRenderDefault))
	}

	outPath := filepath.Join(dir, "out-256.png")
	probes, ffmpeg, args := 0, 0, []string(nil)
	c := AVRenderConverter{AVConverter: AVConverter{run: countedScript(t, "5", 320, 240, &probes, &ffmpeg, &args)}}
	if err := c.Convert(context.Background(), inPath, outPath, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	assertRenderFrame(t, args, probes, ffmpeg, "1", inPath, outPath, avRenderFilter(256))

	for _, ceiling := range []struct {
		duration string
		height   int
	}{
		{duration: "5", height: 5000},
		{duration: "14401", height: 240},
	} {
		blocked := filepath.Join(dir, "blocked-"+ceiling.duration+".png")
		if err := os.WriteFile(blocked, []byte("pre"), 0o600); err != nil {
			t.Fatal(err)
		}
		ffmpeg := 0
		run := func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "ffmpeg" {
				ffmpeg++
				return nil, errors.New("ffmpeg")
			}
			return fakeProbe(args, ceiling.duration, "h264", "aac", 320, ceiling.height)
		}
		err := (AVRenderConverter{AVConverter: AVConverter{run: run}}).Convert(context.Background(), inPath, blocked, nil)
		if err == nil || ffmpeg != 0 || !strings.Contains(err.Error(), "exceeds ceiling") {
			t.Fatalf("ffmpeg %d err %v", ffmpeg, err)
		}
		assertRemoved(t, blocked)
	}
}

func TestAVRenderLetterbox(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		w, h, boxW, boxH, boxX, boxY int
	}{
		{w: 40, h: 20, boxW: 256, boxH: 128, boxX: 0, boxY: 64},
		{w: 2000, h: 1000, boxW: 256, boxH: 128, boxX: 0, boxY: 64},
		{w: 20, h: 40, boxW: 128, boxH: 256, boxX: 64, boxY: 0},
		{w: 100, h: 33, boxW: 256, boxH: 84, boxX: 0, boxY: 86},
	}
	for _, tc := range cases {
		src := renderColorMP4(t, dir, "red", tc.w, tc.h)
		outPath := filepath.Join(dir, fmt.Sprintf("fit-%dx%d.png", tc.w, tc.h))
		if err := (AVRenderConverter{}).Convert(context.Background(), src, outPath, map[string]any{"size": 256}); err != nil {
			t.Fatal(err)
		}
		img := decodePNG(t, outPath)
		if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
			t.Fatalf("%dx%d canvas %v", tc.w, tc.h, img.Bounds())
		}
		x, y, w, h := nonWhiteBox(img)
		if x != tc.boxX || y != tc.boxY || w != tc.boxW || h != tc.boxH {
			t.Fatalf("%dx%d box (%d,%d) %dx%d", tc.w, tc.h, x, y, w, h)
		}
		if w == 256 && h == 256 {
			t.Fatalf("%dx%d filled the square", tc.w, tc.h)
		}
		assertCornersWhite(t, img)
		if whiteAt(img, img.Bounds().Dx()/2, img.Bounds().Dy()/2) {
			t.Fatalf("%dx%d center is white", tc.w, tc.h)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "fit.png" {
			t.Fatal("fit.png")
		}
	}
}

func TestAVRenderUnserved(t *testing.T) {
	for _, pair := range []Pair{
		{From: "gif", To: "png"},
		{From: "pdf", To: "png"},
		{From: "mp4", To: "jpg"},
		{From: "mp3", To: "png"},
	} {
		if _, ok := Default.LookupOp(OperationRender, pair.From, pair.To); ok {
			t.Fatalf("served %s to %s", pair.From, pair.To)
		}
	}
	dir := t.TempDir()
	calls := 0
	c := AVRenderConverter{AVConverter: AVConverter{run: func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("called")
	}}}
	for _, pair := range []Pair{{From: "gif", To: "png"}, {From: "mp4", To: "jpg"}} {
		outPath := filepath.Join(dir, "out."+pair.To)
		if err := os.WriteFile(outPath, []byte("pre"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := c.Convert(context.Background(), filepath.Join(dir, "in."+pair.From), outPath, nil)
		if err == nil || calls != 0 {
			t.Fatalf("%s to %s calls %d err %v", pair.From, pair.To, calls, err)
		}
		assertRemoved(t, outPath)
	}
}

func TestAVRenderSize(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.mp4")
	accept := []map[string]any{
		nil,
		{},
		{"size": 256},
		{"size": int32(256)},
		{"size": int64(256)},
		{"size": float64(256)},
		{"size": 2048},
		{"size": int32(2048)},
		{"size": int64(2048)},
		{"size": float64(2048)},
	}
	for _, opts := range accept {
		edge := avRenderDefault
		if len(opts) == 1 {
			n, err := avRenderSizeInt(opts["size"])
			if err != nil {
				t.Fatal(err)
			}
			edge = n
		}
		outPath := filepath.Join(dir, fmt.Sprintf("ok-%d.png", edge))
		probes, ffmpeg, args := 0, 0, []string(nil)
		c := AVRenderConverter{AVConverter: AVConverter{run: countedScript(t, "5", 40, 20, &probes, &ffmpeg, &args)}}
		if err := c.Convert(context.Background(), inPath, outPath, opts); err != nil {
			t.Fatalf("%#v %v", opts, err)
		}
		if probes != 3 || ffmpeg != 1 || !containsArg(args, "-vf", avRenderFilter(edge)) {
			t.Fatalf("%#v probes %d ffmpeg %d args %v", opts, probes, ffmpeg, args)
		}
	}

	reject := []map[string]any{
		{"size": 255},
		{"size": 2049},
		{"size": 256.5},
		{"size": math.NaN()},
		{"size": math.Inf(1)},
		{"size": "256"},
		{"size": true},
		{"timecode": 1.0},
		{"resolution_height": 720},
		{"codec": "h264"},
		{"size": 256, "timecode": 1.0},
	}
	for i, opts := range reject {
		outPath := filepath.Join(dir, fmt.Sprintf("bad-%d.png", i))
		if err := os.WriteFile(outPath, []byte("pre"), 0o600); err != nil {
			t.Fatal(err)
		}
		calls := 0
		c := AVRenderConverter{AVConverter: AVConverter{run: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			return nil, errors.New("called")
		}}}
		err := c.Convert(context.Background(), inPath, outPath, opts)
		if err == nil || calls != 0 {
			t.Fatalf("%#v calls %d err %v", opts, calls, err)
		}
		assertRemoved(t, outPath)
	}
}

func TestAVRenderBlackFrame(t *testing.T) {
	dir := t.TempDir()
	src := renderColorMP4(t, dir, "black", 40, 20)
	outPath := filepath.Join(dir, "black.png")
	if err := (AVRenderConverter{}).Convert(context.Background(), src, outPath, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, outPath)
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
		t.Fatalf("canvas %v", img.Bounds())
	}
	cx, cy := img.Bounds().Dx()/2, img.Bounds().Dy()/2
	r, g, b := rgbAt(img, cx, cy)
	if r != 0 || g != 0 || b != 0 {
		t.Fatalf("center %d,%d,%d", r, g, b)
	}
	assertCornersWhite(t, img)
	if allWhite(img) {
		t.Fatal("all white")
	}
}

func TestAVRenderNoVideo(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "audio.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1", "-c:a", "aac", src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("audio source: %v\n%s", err, out)
	}
	outPath := filepath.Join(dir, "out.png")
	if err := os.WriteFile(outPath, []byte("pre"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpeg := 0
	c := AVRenderConverter{AVConverter: AVConverter{run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "ffmpeg" {
			ffmpeg++
			return nil, errors.New("ffmpeg started")
		}
		if name != "ffprobe" {
			return nil, fmt.Errorf("unexpected %s", name)
		}
		return execOutput(ctx, name, args...)
	}}}
	err = c.Convert(context.Background(), src, outPath, nil)
	if err == nil || ffmpeg != 0 || !strings.Contains(err.Error(), "no video stream") {
		t.Fatalf("ffmpeg %d err %v", ffmpeg, err)
	}
	assertRemoved(t, outPath)
}

func TestAVRenderFFmpegDown(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.png")
	c := AVRenderConverter{AVConverter: AVConverter{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "ffprobe" {
			return fakeProbe(args, "5", "h264", "aac", 40, 20)
		}
		if name != "ffmpeg" {
			return nil, fmt.Errorf("unexpected %s", name)
		}
		last := strings.TrimPrefix(args[len(args)-1], "file:")
		body := append([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0x00)
		if err := os.WriteFile(last, body, 0o600); err != nil {
			return nil, err
		}
		return nil, errors.New("down")
	}}}
	err := c.Convert(context.Background(), filepath.Join(dir, "in.mp4"), outPath, nil)
	if err == nil || !strings.Contains(err.Error(), "thumbnail failed") {
		t.Fatal(err)
	}
	assertRemoved(t, outPath)
}

func countedScript(t *testing.T, duration string, width, height int, probes, ffmpeg *int, args *[]string) func(context.Context, string, ...string) ([]byte, error) {
	t.Helper()
	inner := scripted(t, duration, "h264", "aac", width, height, args)
	return func(ctx context.Context, name string, argv ...string) ([]byte, error) {
		switch name {
		case "ffprobe":
			*probes++
		case "ffmpeg":
			*ffmpeg++
		}
		return inner(ctx, name, argv...)
	}
}

func assertRenderFrame(t *testing.T, args []string, probes, ffmpeg int, seek, inPath, outPath, filter string) {
	t.Helper()
	if probes != 3 || ffmpeg != 1 {
		t.Fatalf("probes %d ffmpeg %d", probes, ffmpeg)
	}
	ssAt, inAt, vf := -1, -1, 0
	for i, arg := range args {
		if arg == "-ss" {
			ssAt = i
		}
		if arg == "-i" {
			inAt = i
		}
		if arg == "-vf" {
			vf++
		}
		if arg == "-noautorotate" || strings.Contains(arg, "force_original_aspect_ratio") {
			t.Fatalf("arg %s", arg)
		}
	}
	if ssAt < 0 || inAt < 0 || ssAt > inAt || args[ssAt+1] != seek {
		t.Fatalf("seek %v", args)
	}
	if vf != 1 || !containsArg(args, "-vf", filter) {
		t.Fatalf("filter %v", args)
	}
	if !containsArg(args, "-map", "0:1") || !containsArg(args, "-frames:v", "1") || !containsArg(args, "-c:v", "png") {
		t.Fatalf("frame %v", args)
	}
	if !containsArg(args, "-protocol_whitelist", "file,crypto") || !containsArg(args, "-i", "file:"+inPath) || args[len(args)-1] != "file:"+outPath {
		t.Fatalf("paths %v", args)
	}
}

func renderColorMP4(t *testing.T, dir, color string, w, h int) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("%s-%dx%d.mp4", color, w, h))
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", fmt.Sprintf("color=c=%s:s=%dx%d:r=1", color, w, h), "-t", "3", "-c:v", "png", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source %s %dx%d: %v\n%s", color, w, h, err, out)
	}
	return path
}

func decodePNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func rgbAt(img image.Image, x, y int) (uint8, uint8, uint8) {
	r, g, b, _ := img.At(x, y).RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}

func whiteAt(img image.Image, x, y int) bool {
	r, g, b := rgbAt(img, x, y)
	return r == 255 && g == 255 && b == 255
}

func assertCornersWhite(t *testing.T, img image.Image) {
	t.Helper()
	b := img.Bounds()
	for _, p := range []image.Point{
		{X: b.Min.X, Y: b.Min.Y},
		{X: b.Max.X - 1, Y: b.Min.Y},
		{X: b.Min.X, Y: b.Max.Y - 1},
		{X: b.Max.X - 1, Y: b.Max.Y - 1},
	} {
		if !whiteAt(img, p.X, p.Y) {
			r, g, b := rgbAt(img, p.X, p.Y)
			t.Fatalf("corner %d,%d is %d,%d,%d", p.X, p.Y, r, g, b)
		}
	}
}

func nonWhiteBox(img image.Image) (x, y, w, h int) {
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X-1, b.Min.Y-1
	for py := b.Min.Y; py < b.Max.Y; py++ {
		for px := b.Min.X; px < b.Max.X; px++ {
			if whiteAt(img, px, py) {
				continue
			}
			if px < minX {
				minX = px
			}
			if py < minY {
				minY = py
			}
			if px > maxX {
				maxX = px
			}
			if py > maxY {
				maxY = py
			}
		}
	}
	if maxX < minX || maxY < minY {
		return 0, 0, 0, 0
	}
	return minX, minY, maxX - minX + 1, maxY - minY + 1
}

func allWhite(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if !whiteAt(img, x, y) {
				return false
			}
		}
	}
	return true
}

func execOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%w: %s", err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

func assertRemoved(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s remains: %v", path, err)
	}
}
