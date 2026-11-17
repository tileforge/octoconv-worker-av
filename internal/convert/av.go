package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func init() {
	Default.Register(AVConverter{})
}

// AVConverter shells out to ffmpeg and ffprobe.
// A zero value runs those binaries directly.
type AVConverter struct {
	threads int
	run     func(ctx context.Context, name string, args ...string) ([]byte, error)
}

const (
	avMaxSourceDuration         = 4 * time.Hour
	avMaxSourceResolutionHeight = 4320
	avMaxReencodeSourceHeight   = 1080
	avMaxReencodeSourceWidth    = 1920
	avDefaultThumbnailTimecode  = 1.0
	avProbeTimeout              = 15 * time.Second
	x264DefaultCRF              = 23
	maxSaneDurationSeconds      = 1 << 31
)

var (
	avThreads                 int
	avTranscodeToMP4Sources   = []string{"mov", "avi", "mkv", "webm"}
	avExtractSources          = []string{"mp4", "mov", "avi", "mkv", "webm"}
	avAudioExtractTargets     = []string{"mp3", "wav", "m4a"}
	avThumbnailExtractTargets = []string{"jpg", "png", "webp"}
)

// SetAVThreads stores the ffmpeg thread count used when a converter has none of its own.
func SetAVThreads(n int) {
	avThreads = n
}

// Pairs returns every av pair in manifest order.
func (AVConverter) Pairs() []Pair {
	pairs := make([]Pair, 0,
		len(avTranscodeToMP4Sources)+1+
			len(avExtractSources)*(len(avAudioExtractTargets)+len(avThumbnailExtractTargets)))
	for _, from := range avTranscodeToMP4Sources {
		pairs = append(pairs, Pair{From: from, To: "mp4"})
	}
	pairs = append(pairs, Pair{From: "mp4", To: "webm"})
	for _, from := range avExtractSources {
		for _, to := range avAudioExtractTargets {
			pairs = append(pairs, Pair{From: from, To: to})
		}
	}
	for _, from := range avExtractSources {
		for _, to := range avThumbnailExtractTargets {
			pairs = append(pairs, Pair{From: from, To: to})
		}
	}
	return pairs
}

// Convert runs one empty-options av pair. A non-empty opts map is refused
// before ffprobe or ffmpeg runs: timecode, resolution_height, and codec are
// not on a protocol assignment.
func (c AVConverter) Convert(ctx context.Context, inPath, outPath string, opts map[string]any) error {
	if len(opts) != 0 {
		return fmt.Errorf("av: unsupported option")
	}
	target := NormalizeFormat(filepath.Ext(outPath))
	switch target {
	case "mp4", "webm", "mp3", "wav", "m4a", "jpg", "png", "webp":
	default:
		return fmt.Errorf("av: unsupported target format %q", target)
	}
	probeCtx, cancel := context.WithTimeout(ctx, avProbeTimeout)
	defer cancel()
	src, err := c.probe(probeCtx, inPath)
	if err != nil {
		return fmt.Errorf("av: %w", err)
	}
	if err := enforceMaxDuration(src.duration); err != nil {
		return fmt.Errorf("av: %w", err)
	}
	if err := enforceMaxHeight(src.streams); err != nil {
		return fmt.Errorf("av: %w", err)
	}
	switch target {
	case "mp4", "webm":
		return c.transcode(ctx, inPath, outPath, target, src)
	case "mp3", "wav", "m4a":
		return c.extractAudio(ctx, inPath, outPath, target, src)
	default:
		return c.thumbnail(ctx, inPath, outPath, target, src)
	}
}

func (c AVConverter) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.run != nil {
		return c.run(ctx, name, args...)
	}
	return runCommand(ctx, engineEnv(""), name, args...)
}

func (c AVConverter) threadCount() int {
	if c.threads > 0 {
		return c.threads
	}
	if avThreads > 0 {
		return avThreads
	}
	return runtime.NumCPU()
}

type avVideoStream struct {
	index       int
	codec       string
	width       int
	height      int
	attachedPic bool
}

type avSource struct {
	duration   time.Duration
	streams    []avVideoStream
	primary    avVideoStream
	audioCodec string
}

func (c AVConverter) probe(ctx context.Context, inPath string) (avSource, error) {
	durOut, err := c.exec(ctx, "ffprobe", ffprobeDurationArgs(inPath)...)
	if err != nil {
		return avSource{}, fmt.Errorf("ffprobe: %w", err)
	}
	dur, err := parseProbedDuration(string(durOut))
	if err != nil {
		return avSource{}, err
	}
	streamOut, err := c.exec(ctx, "ffprobe", ffprobeStreamArgs(inPath)...)
	if err != nil {
		return avSource{}, fmt.Errorf("ffprobe: %w", err)
	}
	streams, err := parseVideoStreams(streamOut)
	if err != nil {
		return avSource{}, err
	}
	primary, ok := primaryVideo(streams)
	if !ok {
		return avSource{}, fmt.Errorf("ffprobe: no video stream found")
	}
	audioOut, err := c.exec(ctx, "ffprobe", ffprobeAudioCodecArgs(inPath)...)
	if err != nil {
		return avSource{}, fmt.Errorf("ffprobe: %w", err)
	}
	return avSource{
		duration:   dur,
		streams:    streams,
		primary:    primary,
		audioCodec: strings.TrimSpace(string(audioOut)),
	}, nil
}

func parseProbedDuration(raw string) (time.Duration, error) {
	secs, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe: unparseable duration %q: %w", raw, err)
	}
	if math.IsNaN(secs) || math.IsInf(secs, 0) || secs < 0 || secs > maxSaneDurationSeconds {
		return 0, fmt.Errorf("ffprobe: implausible duration %v", secs)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

func parseVideoStreams(raw []byte) ([]avVideoStream, error) {
	var probe struct {
		Streams []struct {
			Index       int    `json:"index"`
			CodecName   string `json:"codec_name"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("ffprobe: unparseable stream probe output: %w", err)
	}
	if len(probe.Streams) == 0 {
		return nil, fmt.Errorf("ffprobe: no video stream found")
	}
	streams := make([]avVideoStream, 0, len(probe.Streams))
	for _, s := range probe.Streams {
		if s.Width <= 0 || s.Height <= 0 {
			return nil, fmt.Errorf("ffprobe: implausible resolution %dx%d", s.Width, s.Height)
		}
		streams = append(streams, avVideoStream{
			index:       s.Index,
			codec:       s.CodecName,
			width:       s.Width,
			height:      s.Height,
			attachedPic: s.Disposition.AttachedPic == 1,
		})
	}
	return streams, nil
}

func primaryVideo(streams []avVideoStream) (avVideoStream, bool) {
	best, found := avVideoStream{}, false
	consider := func(s avVideoStream) {
		if !found || s.width*s.height > best.width*best.height {
			best, found = s, true
		}
	}
	for _, s := range streams {
		if !s.attachedPic {
			consider(s)
		}
	}
	if found {
		return best, true
	}
	for _, s := range streams {
		consider(s)
	}
	return best, found
}

func enforceMaxDuration(d time.Duration) error {
	if d > avMaxSourceDuration {
		return fmt.Errorf("declared duration %v exceeds ceiling %v", d, avMaxSourceDuration)
	}
	return nil
}

func enforceMaxHeight(streams []avVideoStream) error {
	max := 0
	for _, s := range streams {
		if s.height > max {
			max = s.height
		}
	}
	if max > avMaxSourceResolutionHeight {
		return fmt.Errorf("declared height %d exceeds ceiling %d", max, avMaxSourceResolutionHeight)
	}
	return nil
}

func enforceReencodeBound(width, height int) error {
	if height > avMaxReencodeSourceHeight || width > avMaxReencodeSourceWidth {
		return fmt.Errorf("re-encode source resolution %dx%d exceeds %dx%d bound", width, height, avMaxReencodeSourceWidth, avMaxReencodeSourceHeight)
	}
	return nil
}

// ffmpeg treats a bare path as a URL. The file: prefix and the protocol
// whitelist keep a crafted name from selecting another protocol.
func avInputArgs(inPath string) []string {
	return []string{
		"-y", "-nostdin", "-protocol_whitelist", "file,crypto",
		"-i", "file:" + inPath,
	}
}

func ffprobeDurationArgs(path string) []string {
	return []string{"-v", "error", "-show_entries",
		"format=duration", "-of", "default=noprint_wrappers=1:nokey=1",
		"-protocol_whitelist", "file,crypto", "file:" + path}
}

func ffprobeStreamArgs(path string) []string {
	return []string{"-v", "error", "-select_streams", "v",
		"-show_entries", "stream=index,codec_name,width,height:stream_disposition=attached_pic",
		"-of", "json", "-protocol_whitelist", "file,crypto", "file:" + path}
}

func ffprobeAudioCodecArgs(path string) []string {
	return []string{"-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0",
		"-protocol_whitelist", "file,crypto", "file:" + path}
}

func streamCopyLegal(target, videoCodec, audioCodec string) bool {
	switch target {
	case "mp4":
		return videoCodec == "h264" && audioCodec == "aac"
	case "webm":
		return videoCodec == "vp9" && audioCodec == "opus"
	default:
		return false
	}
}

func streamCopyArgs(inPath, outPath, target string, videoIndex int) []string {
	args := append(avInputArgs(inPath),
		"-map", "0:"+strconv.Itoa(videoIndex), "-map", "0:a:0",
		"-c", "copy",
	)
	if target == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	return append(args, "file:"+outPath)
}

func transcodeArgs(inPath, outPath, target string, videoIndex, threads int) []string {
	args := append(avInputArgs(inPath),
		"-map", "0:"+strconv.Itoa(videoIndex), "-map", "0:a:0?")
	switch target {
	case "mp4":
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", strconv.Itoa(x264DefaultCRF),
			"-c:a", "aac", "-b:a", "128k",
			"-movflags", "+faststart",
		)
	case "webm":
		args = append(args,
			"-c:v", "libvpx-vp9", "-b:v", "1M",
			"-c:a", "libopus",
		)
	default:
		return nil
	}
	return append(args, "-threads", strconv.Itoa(threads), "file:"+outPath)
}

func extractAudioArgs(inPath, outPath, target string, streamCopy bool) []string {
	args := append(avInputArgs(inPath), "-vn")
	if streamCopy {
		args = append(args, "-c:a", "copy")
	} else {
		switch target {
		case "mp3":
			args = append(args, "-c:a", "libmp3lame", "-q:a", "2")
		case "wav":
			args = append(args, "-c:a", "pcm_s16le")
		case "m4a":
			args = append(args, "-c:a", "aac", "-b:a", "128k")
		default:
			return nil
		}
	}
	return append(args, "file:"+outPath)
}

func thumbnailArgs(inPath, outPath, target string, timecode float64, videoIndex int) []string {
	args := []string{
		"-y", "-nostdin", "-protocol_whitelist", "file,crypto",
		"-ss", strconv.FormatFloat(timecode, 'f', -1, 64),
		"-i", "file:" + inPath,
		"-map", "0:" + strconv.Itoa(videoIndex),
		"-frames:v", "1",
	}
	switch target {
	case "jpg":
		args = append(args, "-c:v", "mjpeg", "-q:v", "2")
	case "png":
		args = append(args, "-c:v", "png")
	case "webp":
		args = append(args, "-c:v", "libwebp")
	default:
		return nil
	}
	return append(args, "file:"+outPath)
}

func (c AVConverter) transcode(ctx context.Context, inPath, outPath, target string, src avSource) error {
	var args []string
	if streamCopyLegal(target, src.primary.codec, src.audioCodec) {
		args = streamCopyArgs(inPath, outPath, target, src.primary.index)
	} else {
		if err := enforceReencodeBound(src.primary.width, src.primary.height); err != nil {
			return fmt.Errorf("av: %w", err)
		}
		args = transcodeArgs(inPath, outPath, target, src.primary.index, c.threadCount())
		if args == nil {
			return fmt.Errorf("av: unsupported transcode target %q", target)
		}
	}
	if _, err := c.exec(ctx, "ffmpeg", args...); err != nil {
		return fmt.Errorf("av: transcode failed: %w", err)
	}
	return validateAVOutput(outPath, target)
}

func (c AVConverter) extractAudio(ctx context.Context, inPath, outPath, target string, src avSource) error {
	args := extractAudioArgs(inPath, outPath, target, target == "m4a" && src.audioCodec == "aac")
	if args == nil {
		return fmt.Errorf("av: unsupported audio-extract target %q", target)
	}
	if _, err := c.exec(ctx, "ffmpeg", args...); err != nil {
		return fmt.Errorf("av: audio-extract failed: %w", err)
	}
	return validateAVOutput(outPath, target)
}

func (c AVConverter) thumbnail(ctx context.Context, inPath, outPath, target string, src avSource) error {
	seconds := src.duration.Seconds()
	timecode := math.Min(avDefaultThumbnailTimecode, seconds/2)
	args := thumbnailArgs(inPath, outPath, target, timecode, src.primary.index)
	if args == nil {
		return fmt.Errorf("av: unsupported thumbnail target %q", target)
	}
	if _, err := c.exec(ctx, "ffmpeg", args...); err != nil {
		return fmt.Errorf("av: thumbnail failed: %w", err)
	}
	return validateAVOutput(outPath, target)
}

func validateAVOutput(path, target string) error {
	fi, err := statOutput(path)
	if err != nil || fi.Size() == 0 {
		return fmt.Errorf("av: output missing or empty")
	}
	switch target {
	case "jpg", "png", "webp":
		header, err := readHeader(path, 12)
		if err != nil {
			return fmt.Errorf("av: thumbnail not a valid %s: %w", target, err)
		}
		if !thumbnailHeaderOK(target, header) {
			return fmt.Errorf("av: thumbnail not a valid %s", target)
		}
	}
	return nil
}

func thumbnailHeaderOK(target string, header []byte) bool {
	switch target {
	case "jpg":
		return bytes.HasPrefix(header, []byte{0xff, 0xd8, 0xff})
	case "png":
		return bytes.HasPrefix(header, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	case "webp":
		return len(header) >= 12 && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP"))
	default:
		return false
	}
}
