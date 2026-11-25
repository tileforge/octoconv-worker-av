package convert

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

func init() {
	Default.Register(AVRenderConverter{})
}

const (
	avRenderDefault = 1280
	avRenderMin     = 256
	avRenderMax     = 2048
)

// AVRenderConverter draws one video frame as a primary square PNG.
type AVRenderConverter struct {
	AVConverter
}

// Operation reports the render operation.
func (AVRenderConverter) Operation() string { return OperationRender }

// Pairs returns mp4, mov, avi, mkv, and webm, each to png.
func (AVRenderConverter) Pairs() []Pair {
	pairs := make([]Pair, 0, len(avExtractSources))
	for _, from := range avExtractSources {
		pairs = append(pairs, Pair{From: from, To: "png"})
	}
	return pairs
}

// Convert writes one square PNG at outPath.
// A nil or empty opts map uses edge 1280. The only accepted key is size.
func (c AVRenderConverter) Convert(ctx context.Context, inPath, outPath string, opts map[string]any) error {
	if err := c.draw(ctx, inPath, outPath, opts); err != nil {
		_ = os.Remove(outPath)
		return err
	}
	return nil
}

func (c AVRenderConverter) draw(ctx context.Context, inPath, outPath string, opts map[string]any) error {
	edge, err := avRenderEdge(opts)
	if err != nil {
		return err
	}
	srcExt := NormalizeFormat(filepath.Ext(inPath))
	tgtExt := NormalizeFormat(filepath.Ext(outPath))
	if !avRenderPair(srcExt, tgtExt) {
		return fmt.Errorf("unsupported conversion %s -> %s", srcExt, tgtExt)
	}
	host := c.host()
	host.workDir = filepath.Dir(outPath)
	probeCtx, cancel := context.WithTimeout(ctx, avProbeTimeout)
	defer cancel()
	src, err := host.probe(probeCtx, inPath)
	if err != nil {
		return fmt.Errorf("av: %w", err)
	}
	if err := enforceMaxDuration(src.duration); err != nil {
		return fmt.Errorf("av: %w", err)
	}
	if err := enforceMaxHeight(src.streams); err != nil {
		return fmt.Errorf("av: %w", err)
	}
	timecode := math.Min(avDefaultThumbnailTimecode, src.duration.Seconds()/2)
	args := []string{
		"-y", "-nostdin", "-protocol_whitelist", "file,crypto",
		"-ss", strconv.FormatFloat(timecode, 'f', -1, 64),
		"-i", "file:" + inPath,
		"-map", "0:" + strconv.Itoa(src.primary.index),
		"-frames:v", "1",
		"-vf", avRenderFilter(edge),
		"-c:v", "png",
		"file:" + outPath,
	}
	if _, err := host.exec(ctx, "ffmpeg", args...); err != nil {
		return fmt.Errorf("av: thumbnail failed: %w", err)
	}
	if err := validateAVOutput(outPath, "png"); err != nil {
		return fmt.Errorf("av: %w", err)
	}
	return nil
}

func (c AVRenderConverter) host() AVConverter {
	if c.run != nil || c.sandbox != nil {
		return c.AVConverter
	}
	live, ok := Default.LookupOp(OperationConvert, "mp4", "png")
	if !ok {
		return c.AVConverter
	}
	host, ok := live.(AVConverter)
	if !ok || (host.run == nil && host.sandbox == nil) {
		return c.AVConverter
	}
	return host
}

func avRenderPair(from, to string) bool {
	if to != "png" {
		return false
	}
	for _, src := range avExtractSources {
		if src == from {
			return true
		}
	}
	return false
}

func avRenderEdge(opts map[string]any) (int, error) {
	if len(opts) == 0 {
		return avRenderDefault, nil
	}
	if len(opts) != 1 {
		return 0, fmt.Errorf("av render: unsupported option")
	}
	raw, ok := opts["size"]
	if !ok {
		return 0, fmt.Errorf("av render: unsupported option")
	}
	n, err := avRenderSizeInt(raw)
	if err != nil {
		return 0, err
	}
	if n < avRenderMin || n > avRenderMax {
		return 0, fmt.Errorf("av render: size %d out of range", n)
	}
	return n, nil
}

func avRenderSizeInt(raw any) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case int32:
		return int(v), nil
	case int64:
		return int(v), nil
	case float64:
		if v != math.Trunc(v) || math.IsInf(v, 0) || math.IsNaN(v) {
			return 0, fmt.Errorf("av render: size is not an integer")
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("av render: size is not an integer")
	}
}

func avRenderFilter(edge int) string {
	e := strconv.Itoa(edge)
	// iw and ih are coded pixels. scale preserves display aspect by setting a non-1 SAR.
	return "scale=w='if(gt(iw*sar,ih)," + e + ",if(gt(ih,iw*sar),trunc(iw*sar*" + e + "/ih)," + e + "))':h='if(gt(ih,iw*sar)," + e + ",if(gt(iw*sar,ih),trunc(ih*" + e + "/(iw*sar))," + e + "))':flags=lanczos,setsar=1,pad=w=" + e + ":h=" + e + ":x='(ow-iw)/2':y='(oh-ih)/2':color=white"
}
