// Package convert turns one video file into another container, an audio track, or a still with ffmpeg and ffprobe.
package convert

import (
	"context"
	"strings"
	"time"
)

const (
	EngineAV                   = "av"
	OperationConvert           = "convert"
	DefaultEngineTimeout       = 753 * time.Second
	MaxFileBytes         int64 = 2 << 30
)

// Pair is one source format and one target format.
type Pair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Converter converts the file at inPath into outPath.
type Converter interface {
	Pairs() []Pair
	Convert(ctx context.Context, inPath, outPath string, opts map[string]any) error
}

// MIMEType is the content type the core stores for format.
func MIMEType(format string) string {
	switch NormalizeFormat(format) {
	case "png":
		return "image/png"
	case "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "m4a":
		return "audio/mp4"
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	default:
		return "application/octet-stream"
	}
}

// NormalizeFormat lowercases a format or extension and strips one leading dot.
func NormalizeFormat(f string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(f), "."))
}

type opKey struct {
	Operation string
	From      string
	To        string
}

// Registry maps an operation and a format pair to a converter.
type Registry struct {
	m map[opKey]Converter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{m: make(map[opKey]Converter)}
}

// Register indexes each pair c reports.
func (r *Registry) Register(c Converter) {
	for _, pair := range c.Pairs() {
		r.m[opKey{
			Operation: OperationConvert,
			From:      NormalizeFormat(pair.From),
			To:        NormalizeFormat(pair.To),
		}] = c
	}
}

// LookupOp reports the converter for op, from, and to.
func (r *Registry) LookupOp(op, from, to string) (Converter, bool) {
	c, ok := r.m[opKey{
		Operation: op,
		From:      NormalizeFormat(from),
		To:        NormalizeFormat(to),
	}]
	return c, ok
}

// Default is the process registry. init registers AVConverter.
var Default = NewRegistry()
