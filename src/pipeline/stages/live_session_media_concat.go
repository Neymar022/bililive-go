package stages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/bililive-go/bililive-go/src/pkg/utils"
	"github.com/bililive-go/bililive-go/src/subtitle"
)

// 只把解码参数放入签名；时长、码率、帧数、文件标签不是流兼容性参数。
type liveSessionStreamSignature struct {
	CodecType        string          `json:"codec_type"`
	CodecName        string          `json:"codec_name"`
	CodecTag         string          `json:"codec_tag_string"`
	Profile          string          `json:"profile"`
	Level            int             `json:"level"`
	TimeBase         string          `json:"time_base"`
	ExtradataHash    string          `json:"extradata_hash"`
	ExtradataSize    int             `json:"extradata_size"`
	Width            int             `json:"width"`
	Height           int             `json:"height"`
	CodedWidth       int             `json:"coded_width"`
	CodedHeight      int             `json:"coded_height"`
	PixelFormat      string          `json:"pix_fmt"`
	SAR              string          `json:"sample_aspect_ratio"`
	FrameRate        string          `json:"r_frame_rate"`
	AverageFrameRate string          `json:"avg_frame_rate"`
	FieldOrder       string          `json:"field_order"`
	ColorRange       string          `json:"color_range"`
	ColorSpace       string          `json:"color_space"`
	ColorTransfer    string          `json:"color_transfer"`
	ColorPrimaries   string          `json:"color_primaries"`
	ChromaLocation   string          `json:"chroma_location"`
	HasBFrames       int             `json:"has_b_frames"`
	Refs             int             `json:"refs"`
	SampleFormat     string          `json:"sample_fmt"`
	SampleRate       string          `json:"sample_rate"`
	Channels         int             `json:"channels"`
	ChannelLayout    string          `json:"channel_layout"`
	BitsPerSample    int             `json:"bits_per_sample"`
	BitsPerRawSample string          `json:"bits_per_raw_sample"`
	InitialPadding   int             `json:"initial_padding"`
	Disposition      json.RawMessage `json:"disposition"`
	SideData         json.RawMessage `json:"side_data_list"`
}

type liveSessionProbedStream struct {
	liveSessionStreamSignature
	StartTime string `json:"start_time"`
	Duration  string `json:"duration"`
	Frames    string `json:"nb_frames"`
}

type liveSessionMediaContract struct {
	Streams []liveSessionProbedStream `json:"streams"`
	Format  struct {
		Duration  string `json:"duration"`
		StartTime string `json:"start_time"`
	} `json:"format"`
	duration float64
	video    liveSessionProbedStream
	audio    liveSessionProbedStream
}

func liveSessionMediaTools(ctx context.Context, ffmpegPath string) (string, string, error) {
	if ffmpegPath == "" {
		var err error
		ffmpegPath, err = utils.GetFFmpegPath(ctx)
		if err != nil {
			return "", "", err
		}
	}
	// 显式配置和 remotetools 的 ffmpeg 优先级保持不变，探测必须使用配套工具。
	resolved, err := exec.LookPath(ffmpegPath)
	if err != nil {
		return "", "", err
	}
	probeName := "ffprobe" + filepath.Ext(resolved)
	// Unix 路径的版本号不应被当作扩展名。
	if !strings.EqualFold(filepath.Ext(resolved), ".exe") {
		probeName = "ffprobe"
	}
	ffprobePath := filepath.Join(filepath.Dir(resolved), probeName)
	if _, err := exec.LookPath(ffprobePath); err != nil {
		return "", "", fmt.Errorf("live session paired ffprobe unavailable: %w", err)
	}
	return resolved, ffprobePath, nil
}

func liveSessionPositiveNumber(text string) (float64, error) {
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 0, fmt.Errorf("invalid positive media value %q", text)
	}
	return n, nil
}

func liveSessionRatio(text string) (string, float64, error) {
	r, ok := new(big.Rat).SetString(strings.ReplaceAll(text, ":", "/"))
	if !ok || r.Sign() <= 0 {
		return "", 0, fmt.Errorf("invalid media ratio %q", text)
	}
	n, _ := r.Float64()
	if math.IsInf(n, 0) || math.IsNaN(n) {
		return "", 0, fmt.Errorf("invalid media ratio %q", text)
	}
	return r.RatString(), n, nil
}

func probeLiveSessionMediaContract(ctx context.Context, ffprobePath, inputPath string, _ bool) (liveSessionMediaContract, error) {
	var media liveSessionMediaContract
	cmd := exec.CommandContext(ctx, ffprobePath, "-v", "error", "-show_streams", "-show_format", "-show_data_hash", "sha256", "-of", "json", inputPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil || stderr.Len() != 0 {
		return media, fmt.Errorf("live session media probe failed for %s: %v: %s", inputPath, err, trimCommandOutput(stderr.Bytes()))
	}
	if err := json.Unmarshal(data, &media); err != nil {
		return media, fmt.Errorf("live session invalid probe: %w", err)
	}
	if len(media.Streams) != 2 {
		return media, fmt.Errorf("live session requires exactly one video and one audio stream: %s", inputPath)
	}
	videoCount, audioCount := 0, 0
	for _, s := range media.Streams {
		if s.CodecName == "" || s.CodecName == "unknown" || s.Profile == "" || s.Profile == "unknown" || s.CodecTag == "" || s.ExtradataSize <= 0 || !strings.HasPrefix(s.ExtradataHash, "SHA256:") || len(s.ExtradataHash) != 71 {
			return media, fmt.Errorf("live session incomplete codec parameters: %s", inputPath)
		}
		if _, _, err := liveSessionRatio(s.TimeBase); err != nil {
			return media, err
		}
		// 时间戳在视频时间轴上；音轨可延迟开始或先于视频，归一化时按真实 PTS 补/裁。
		start, err := strconv.ParseFloat(s.StartTime, 64)
		if err != nil || math.IsNaN(start) || math.IsInf(start, 0) {
			return media, fmt.Errorf("live session unsupported stream start: %s", inputPath)
		}
		if _, err := liveSessionPositiveNumber(s.Duration); err != nil {
			return media, err
		}
		switch s.CodecType {
		case "video":
			videoCount++
			media.video = s
			if s.Width <= 0 || s.Height <= 0 || s.PixelFormat == "" {
				return media, fmt.Errorf("live session incomplete video parameters: %s", inputPath)
			}
			for _, v := range []string{s.FrameRate, s.AverageFrameRate} {
				if _, _, err := liveSessionRatio(v); err != nil {
					return media, err
				}
			}
			if _, err := liveSessionEffectiveSAR(s.SAR); err != nil {
				return media, err
			}
			if math.Abs(start) > 0.001 {
				return media, fmt.Errorf("live session unsupported video origin: %s", inputPath)
			}
			if s.PixelFormat != "yuv420p" || len(s.SideData) > 0 || (s.ColorTransfer != "" && s.ColorTransfer != "unknown" && s.ColorTransfer != "bt709" && s.ColorTransfer != "smpte170m") {
				return media, fmt.Errorf("live session unsupported video color/side data: %s", inputPath)
			}
		case "audio":
			audioCount++
			media.audio = s
			if len(s.SideData) > 0 {
				return media, fmt.Errorf("live session unsupported audio side data: %s", inputPath)
			}
			if s.SampleFormat == "" || s.Channels <= 0 || s.ChannelLayout == "" {
				return media, fmt.Errorf("live session incomplete audio parameters: %s", inputPath)
			}
			if _, err := liveSessionPositiveNumber(s.SampleRate); err != nil {
				return media, err
			}
		default:
			return media, fmt.Errorf("live session unsupported stream type: %s", s.CodecType)
		}
	}
	if videoCount != 1 || audioCount != 1 {
		return media, fmt.Errorf("live session requires exactly one video and one audio stream: %s", inputPath)
	}
	containerDuration, err := liveSessionPositiveNumber(media.Format.Duration)
	if err != nil {
		return media, err
	}
	start, err := strconv.ParseFloat(media.Format.StartTime, 64)
	if err != nil || math.IsNaN(start) || math.IsInf(start, 0) || math.Abs(start) > 0.001 {
		return media, fmt.Errorf("live session unsupported container start: %s", inputPath)
	}
	vd, _ := liveSessionPositiveNumber(media.video.Duration)
	ad, _ := liveSessionPositiveNumber(media.audio.Duration)
	// 音轨的延迟和自然短尾不是损坏；容器必须覆盖已声明的轨道时间范围。
	audioStart, _ := strconv.ParseFloat(media.audio.StartTime, 64)
	if containerDuration+0.000002 < vd || audioStart+ad > containerDuration+0.001 {
		return media, fmt.Errorf("live session inconsistent container timeline: %s", inputPath)
	}
	media.duration = vd
	return media, nil
}

// 缺省 SAR 表示解码器未声明像素比例；FFmpeg 显示/scale 按方形像素处理。
// 显式零值、N/A 或其他无效值必须拒绝，不能当作缺省。
func liveSessionEffectiveSAR(value string) (string, error) {
	if value == "" {
		return "1", nil
	}
	r, _, err := liveSessionRatio(value)
	return r, err
}

func liveSessionCopyCompatible(inputs []liveSessionMediaContract) bool {
	for _, other := range inputs[1:] {
		for i, s := range inputs[0].Streams {
			if !reflect.DeepEqual(s.liveSessionStreamSignature, other.Streams[i].liveSessionStreamSignature) {
				return false
			}
		}
	}
	return true
}

func normalizeLiveSessionMediaArgs(inputs []string, media []liveSessionMediaContract, outputPath string) ([]string, error) {
	first := media[0].video
	fps, _, err := liveSessionRatio(first.AverageFrameRate)
	if err != nil {
		return nil, err
	}
	sar, err := liveSessionEffectiveSAR(first.SAR)
	if err != nil {
		return nil, err
	}
	if first.Width%2 != 0 || first.Height%2 != 0 {
		return nil, fmt.Errorf("live session unsupported odd canvas")
	}
	args := []string{"-hide_banner", "-nostdin", "-v", "error", "-xerror", "-copyts", "-y"}
	var filters []string
	var videos, audios strings.Builder
	cumulative := 0.0
	previousSample := int64(0)
	for i, path := range inputs {
		v, a := media[i].video, media[i].audio
		if v.FieldOrder != "progressive" || v.ColorSpace != first.ColorSpace || v.ColorTransfer != first.ColorTransfer || v.ColorPrimaries != first.ColorPrimaries || v.ColorRange != first.ColorRange || (a.ChannelLayout != "mono" && a.ChannelLayout != "stereo") {
			return nil, fmt.Errorf("live session unsupported normalization parameters for %s", path)
		}
		frameRate, rate, _ := liveSessionRatio(v.FrameRate)
		averageRate, _, _ := liveSessionRatio(v.AverageFrameRate)
		// 逐源保留其已声明的帧率；帧数存在时额外核对自身时长，不强制 fragmented MP4 提供帧数。
		// 不要求该源落在首源的整数帧网格。
		if frameRate != averageRate {
			return nil, fmt.Errorf("live session unsupported variable frame timeline: %s", path)
		}
		if v.Frames != "" {
			frames, frameErr := strconv.ParseInt(v.Frames, 10, 64)
			if frameErr != nil || frames <= 0 || math.Abs(float64(frames)/rate-media[i].duration) > 0.000002 {
				return nil, fmt.Errorf("live session inconsistent frame duration: %s", path)
			}
		}
		inputSAR, err := liveSessionEffectiveSAR(v.SAR)
		if err != nil {
			return nil, err
		}
		args = append(args, "-i", path)
		cumulative += media[i].duration
		if cumulative > float64(math.MaxInt64)/48000 {
			return nil, fmt.Errorf("live session audio sample count overflow")
		}
		nextSample := int64(math.Round(cumulative * 48000))
		budget := nextSample - previousSample
		previousSample = nextSample
		scale := fmt.Sprintf("scale=w='trunc(min(%d,%d*dar/(%s))/2)*2':h='trunc(min(%d,%d*(%s)/dar)/2)*2'", first.Width, first.Height, sar, first.Height, first.Width, sar)
		filters = append(filters, fmt.Sprintf("[%d:v:0]setpts=PTS-STARTPTS,setsar=%s,%s,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=%s,settb=AVTB[v%d]", i, inputSAR, scale, first.Width, first.Height, sar, i))
		// first_pts=0 按原 PTS 插入前置静音/裁掉视频前的样本；之后才按样本序号归零。
		// 用累计视频边界求样本预算，避免各段独立 round 的误差累积。
		filters = append(filters, fmt.Sprintf("[%d:a:0]aresample=48000:async=1:first_pts=0,aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,apad,atrim=end_sample=%d,asetpts=N/SR/TB[a%d]", i, budget, i))
		fmt.Fprintf(&videos, "[v%d]", i)
		fmt.Fprintf(&audios, "[a%d]", i)
	}
	// 分别拼接，避免 AAC 采样网格决定视频边界；整条视频只做一次 fps 量化。
	filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[vc]", videos.String(), len(inputs)), fmt.Sprintf("%sconcat=n=%d:v=0:a=1[a]", audios.String(), len(inputs)), fmt.Sprintf("[vc]fps=%s:start_time=0:round=near,trim=duration=%.9f[v]", fps, cumulative))
	args = append(args, "-filter_complex", strings.Join(filters, ";"), "-map", "[v]", "-map", "[a]", "-c:v", "libx265", "-preset", "ultrafast", "-crf", "24", "-x265-params", "log-level=error", "-pix_fmt", "yuv420p", "-tag:v", "hvc1", "-c:a", "aac", "-b:a", "128k")
	for _, color := range []struct{ flag, value string }{{"-color_range", first.ColorRange}, {"-colorspace", first.ColorSpace}, {"-color_trc", first.ColorTransfer}, {"-color_primaries", first.ColorPrimaries}} {
		if color.value != "" && color.value != "unknown" {
			args = append(args, color.flag, color.value)
		}
	}
	return append(args, "-movflags", "+faststart", "-f", "mp4", outputPath), nil
}

// gate 在公开文件的事务操作前执行；ffmpeg rc0 不够，还需 error 日志为空且实际有视频帧。
func validateLiveSessionMediaOutput(ctx context.Context, ffmpegPath, ffprobePath, outputPath string, media []liveSessionMediaContract) error {
	output, err := probeLiveSessionMediaContract(ctx, ffprobePath, outputPath, true)
	if err != nil {
		return err
	}
	first := media[0].video
	sourceSAR, _ := liveSessionEffectiveSAR(first.SAR)
	outputSAR, _ := liveSessionEffectiveSAR(output.video.SAR)
	if output.video.Width != first.Width || output.video.Height != first.Height || outputSAR != sourceSAR {
		return fmt.Errorf("live session output canvas mismatch")
	}
	expected := 0.0
	for _, m := range media {
		expected += m.duration
	}
	_, fps, _ := liveSessionRatio(media[0].video.AverageFrameRate)
	tolerance := 1/fps + 0.00001
	if math.Abs(output.duration-expected) > tolerance {
		return fmt.Errorf("live session output duration mismatch: %.9f != %.9f", output.duration, expected)
	}
	// 每个边界前后各至少一秒；短片覆盖到实际末尾，所有窗口均须有帧。
	starts := []float64{0}
	boundary := 0.0
	for _, m := range media[:len(media)-1] {
		boundary += m.duration
		starts = append(starts, math.Max(0, boundary-1))
	}
	starts = append(starts, math.Max(0, expected-1))
	for _, start := range starts {
		duration := math.Min(2, expected-start)
		cmd := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-nostdin", "-v", "error", "-xerror", "-ss", strconv.FormatFloat(start, 'f', 9, 64), "-i", outputPath, "-t", strconv.FormatFloat(duration, 'f', 9, 64), "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-", "-progress", "pipe:1", "-nostats")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		progress, err := cmd.Output()
		frames := int64(0)
		for _, line := range strings.Split(string(progress), "\n") {
			if strings.HasPrefix(line, "frame=") {
				n, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "frame=")), 10, 64)
				if n > frames {
					frames = n
				}
			}
		}
		if err != nil || stderr.Len() > 0 || frames == 0 {
			return fmt.Errorf("live session decode validation failed at %.9f: %v, frames=%d: %s", start, err, frames, trimCommandOutput(stderr.Bytes()))
		}
	}
	return nil
}

func concatLiveSessionMediaSafely(ctx context.Context, ffmpegPath string, inputs []string, outputPath string) (err error) {
	return concatLiveSessionMediaMode(ctx, ffmpegPath, inputs, outputPath, false)
}

func concatLiveSessionMediaResultOnly(ctx context.Context, ffmpegPath string, inputs []string, outputPath string) error {
	return concatLiveSessionMediaMode(ctx, ffmpegPath, inputs, outputPath, true)
}

func concatLiveSessionMediaMode(ctx context.Context, ffmpegPath string, inputs []string, outputPath string, resultOnly bool) (err error) {
	if len(inputs) == 0 {
		return fmt.Errorf("live session media concat has no inputs")
	}
	if _, statErr := os.Lstat(outputPath); !os.IsNotExist(statErr) {
		return fmt.Errorf("live session output already exists or cannot be inspected: %s", outputPath)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ffmpegPath, ffprobePath, err := liveSessionMediaTools(ctx, ffmpegPath)
	if err != nil {
		return err
	}
	media := make([]liveSessionMediaContract, len(inputs))
	for i, path := range inputs {
		media[i], err = probeLiveSessionMediaContract(ctx, ffprobePath, path, false)
		if err != nil {
			return err
		}
	}
	var args []string
	if len(inputs) > 1 {
		if liveSessionCopyCompatible(media) {
			list, err := os.CreateTemp(filepath.Dir(outputPath), ".live-session-concat-*.txt")
			if err != nil {
				return err
			}
			defer os.Remove(list.Name())
			for i, path := range inputs {
				if strings.ContainsAny(path, "\r\n") {
					_ = list.Close()
					return fmt.Errorf("live session concat path contains a newline")
				}
				// 显式时长使用同一高精度媒体时间轴，避免 demuxer 的估算影响后段起点。
				if _, err := fmt.Fprintf(list, "file '%s'\nduration %.9f\n", escapeFFmpegConcatPath(path), media[i].duration); err != nil {
					_ = list.Close()
					return err
				}
			}
			if err := list.Close(); err != nil {
				return err
			}
			args = []string{"-hide_banner", "-nostdin", "-v", "error", "-xerror", "-copyts", "-y", "-f", "concat", "-safe", "0", "-i", list.Name(), "-map", "0", "-c", "copy", "-avoid_negative_ts", "disabled", "-f", "mp4", outputPath}
		} else {
			if resultOnly {
				return subtitle.ErrLibraryComputeRequired
			}
			args, err = normalizeLiveSessionMediaArgs(inputs, media, outputPath)
			if err != nil {
				return err
			}
		}
	}
	defer func() {
		if err != nil {
			_ = os.Remove(outputPath)
		}
	}()
	if len(inputs) == 1 {
		if err = os.Link(inputs[0], outputPath); err != nil {
			return err
		}
	} else {
		var output []byte
		output, err = exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
		if err != nil || len(bytes.TrimSpace(output)) > 0 {
			return fmt.Errorf("ffmpeg concat failed: %v: %s", err, trimCommandOutput(output))
		}
	}
	return validateLiveSessionMediaOutput(ctx, ffmpegPath, ffprobePath, outputPath, media)
}
