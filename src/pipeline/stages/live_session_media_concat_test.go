package stages

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

func mediaTestTools(t *testing.T) (string, string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("本机无 ffmpeg，真实媒体测试未运行")
	}
	ffprobe := filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
	if _, err := os.Stat(ffprobe); err != nil {
		t.Skip("本机无配套 ffprobe，真实媒体测试未运行")
	}
	encoders, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").CombinedOutput()
	require.NoError(t, err)
	if !strings.Contains(string(encoders), "libx265") {
		t.Skip("本机无 libx265，真实媒体测试未运行")
	}
	return ffmpeg, ffprobe
}

func mediaTestCommand(t *testing.T, tool string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(tool, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	require.NoError(t, err, "%s %v: %s", tool, args, stderr.String())
	require.Empty(t, stderr.String(), "%s %v", tool, args)
	return output
}

func mediaTestGenerate(t *testing.T, ffmpeg, dir, name, size, color string, filters ...string) string {
	t.Helper()
	path := filepath.Join(dir, name+".mp4")
	// 延迟短音轨与现场相同，片段间不重叠 AAC 预滚包；copy 对照须具备兼容包时钟。
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=" + color + ":s=" + size + ":r=30:d=2", "-itsoffset", "0.1", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1.8"}
	if len(filters) > 0 {
		args = append(args, "-vf", strings.Join(filters, ","))
	}
	args = append(args, "-c:v", "libx265", "-preset", "ultrafast", "-crf", "24", "-x265-params", "pools=1:frame-threads=1:log-level=error", "-pix_fmt", "yuv420p", "-tag:v", "hvc1", "-c:a", "aac", "-y", path)
	mediaTestCommand(t, ffmpeg, args...)
	return path
}

func mediaTestDuration(t *testing.T, ffprobe, path string) float64 {
	t.Helper()
	data := mediaTestCommand(t, ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=duration", "-of", "json", path)
	var result struct {
		Streams []struct {
			Duration string `json:"duration"`
		} `json:"streams"`
	}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Len(t, result.Streams, 1)
	var duration float64
	_, err := fmt.Sscan(result.Streams[0].Duration, &duration)
	require.NoError(t, err)
	return duration
}

func TestLiveSessionMediaConcatRealHEVC(t *testing.T) {
	ffmpeg, ffprobe := mediaTestTools(t)
	dir := t.TempDir()
	a := mediaTestGenerate(t, ffmpeg, dir, "a", "360x640", "red")
	b := mediaTestGenerate(t, ffmpeg, dir, "b", "240x426", "blue")
	tb := filepath.Join(dir, "timebase.mp4")
	mediaTestCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-y", "-i", a, "-c", "copy", "-video_track_timescale", "18432", tb)
	for _, tc := range []struct {
		name   string
		inputs []string
	}{
		{"dimensions", []string{a, b, a}},
		{"timebase", []string{a, tb, a}},
		{"compatible_copy", []string{a, a, a}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(dir, tc.name+"-output.mp4")
			require.NoError(t, concatLiveSessionMediaWithFFmpeg(context.Background(), ffmpeg, tc.inputs, output))
			require.InDelta(t, 6, mediaTestDuration(t, ffprobe, output), 0.08, "媒体时间轴不能因 time_base 不同漂移")
			frames := mediaTestCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-i", output, "-map", "0:v:0", "-f", "framemd5", "-")
			require.Equal(t, 180, strings.Count("\n"+string(frames), "\n0,"), "必须真实解出全部视频帧")
			for _, seek := range []string{"0", "1.9", "2", "3.9", "4", "5.9"} {
				frame := mediaTestCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-ss", seek, "-i", output, "-frames:v", "1", "-vf", "scale=1:1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
				require.Len(t, frame, 3)
				if tc.name == "dimensions" && (seek == "2" || seek == "3.9") {
					require.Greater(t, frame[2], frame[0], "中段必须保持蓝色画面")
				} else {
					require.Greater(t, frame[0], frame[2], "其他段必须保持红色画面")
				}
			}
			if tc.name == "compatible_copy" {
				packets := func(path, stream string) []string {
					data := mediaTestCommand(t, ffprobe, "-v", "error", "-select_streams", stream, "-show_packets", "-show_entries", "packet=data_hash", "-show_data_hash", "sha256", "-of", "json", path)
					var result struct {
						Packets []struct {
							Hash string `json:"data_hash"`
						} `json:"packets"`
					}
					require.NoError(t, json.Unmarshal(data, &result))
					var hashes []string
					for _, p := range result.Packets {
						hashes = append(hashes, p.Hash)
					}
					return hashes
				}
				for _, stream := range []string{"v:0", "a:0"} {
					src := packets(a, stream)
					expected := append(append(append([]string{}, src...), src...), src...)
					require.Equal(t, expected, packets(output, stream), "兼容源必须保留原压缩音视频包")
				}
			}
		})
	}
}

func TestLiveSessionSubtitleTimelineUsesMediaDuration(t *testing.T) {
	old := liveSessionMediaProbeDuration
	liveSessionMediaProbeDuration = func(context.Context, string, string) (float64, error) { return 2, nil }
	t.Cleanup(func() { liveSessionMediaProbeDuration = old })
	original := []subtitle.Segment{
		{Start: "00:00:01,500", End: "00:00:03,000", Text: "跨尾"},
		{Start: "00:00:02,500", End: "00:00:04,000", Text: "尾外"},
	}
	first := subtitle.Metadata{Segments: append([]subtitle.Segment{}, original...)}
	second := subtitle.Metadata{Segments: []subtitle.Segment{{Start: "00:00:00,200", End: "00:00:00,800", Text: "第二段"}}}
	got, mappings, err := mergedLiveSessionSubtitleSegments(context.Background(), "", []knowledgeSessionPayloadInput{{Metadata: &first}, {Metadata: &second}}, []string{"first.mp4", "second.mp4"})
	require.NoError(t, err)
	require.Equal(t, []subtitle.Segment{
		{Index: 1, Start: "00:00:01,500", End: "00:00:02,000", Text: "跨尾"},
		{Index: 2, Start: "00:00:02,200", End: "00:00:02,800", Text: "第二段"},
	}, got)
	require.Equal(t, float64(2), mappings[1]["offset"])
	require.Equal(t, original, first.Segments, "原分段字幕必须保持")
	require.False(t, math.IsNaN(mappings[0]["duration"].(float64)))
}

// 外部工具故障在真正的发布入口验证，不能只看 helper 的返回值。
func TestLiveSessionMediaPublicationRejectsInvalidMedia(t *testing.T) {
	ffmpeg, ffprobe := mediaTestTools(t)
	fixtureDir := t.TempDir()
	a := mediaTestGenerate(t, ffmpeg, fixtureDir, "a", "360x640", "red")
	b := mediaTestGenerate(t, ffmpeg, fixtureDir, "b", "240x426", "blue")
	noAudio := filepath.Join(fixtureDir, "no-audio.mp4")
	noVideo := filepath.Join(fixtureDir, "no-video.mp4")
	mediaTestCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-i", a, "-map", "0:v:0", "-c", "copy", noAudio)
	mediaTestCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-i", a, "-map", "0:a:0", "-c", "copy", noVideo)
	for _, tc := range []struct{ name, first, second, mode, want string }{
		{"probe_failure", "corrupt", a, "", "probe failed"},
		{"missing_audio", noAudio, a, "", "exactly one video and one audio"},
		{"missing_video", noVideo, a, "", "exactly one video and one audio"},
		{"encoder_failure", a, b, "encode_failure", "concat failed"},
		{"encoder_rc0_error", a, b, "encode_error", "concat failed"},
		{"decoder_rc0_error", a, a, "decode_error", "decode validation failed"},
		{"decoder_rc0_no_frames", a, a, "decode_empty", "decode validation failed"},
		{"probe_rc0_error", a, a, "probe_error", "probe failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			library := filepath.Join(t.TempDir(), "video")
			var manifest knowledgeSessionManifest
			manifest.LiveSessionID = "reject-" + tc.name
			var protected []string
			for i, fixture := range []string{tc.first, tc.second} {
				video, metadata, _ := writeCompletedKnowledgeSessionSidecar(t, library, "Host", int64(19+i), "字幕")
				data := []byte("invalid media")
				if fixture != "corrupt" {
					var err error
					data, err = os.ReadFile(fixture)
					require.NoError(t, err)
				}
				require.NoError(t, os.WriteFile(video, data, 0o644))
				manifest.Sources = append(manifest.Sources, knowledgeSessionManifestSource{LibraryPath: video, MetadataPath: metadata})
				protected = append(protected, video, metadata)
			}
			snapshots := make(map[string][]byte)
			for _, path := range protected {
				snapshots[path] = mustReadStageFile(t, path)
			}
			tool := ffmpeg
			if tc.mode != "" {
				dir := t.TempDir()
				tool = filepath.Join(dir, "ffmpeg")
				quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
				script := "#!/bin/sh\n"
				if strings.HasPrefix(tc.mode, "encode_") {
					script += "for arg in \"$@\"; do\n if [ \"$arg\" = '-filter_complex' ]; then\n  echo 'encoder unavailable' >&2\n"
					if tc.mode == "encode_failure" {
						script += "  exit 1\n"
					} else {
						script += "  exit 0\n"
					}
					script += " fi\ndone\n"
				} else if strings.HasPrefix(tc.mode, "decode_") {
					script += "for arg in \"$@\"; do\n if [ \"$arg\" = '-progress' ]; then\n"
					if tc.mode == "decode_error" {
						script += "  echo 'decoder damaged frame' >&2\n  echo frame=1\n"
					} else {
						script += "  echo frame=0\n"
					}
					script += "  exit 0\n fi\ndone\n"
				}
				script += "exec " + quote(ffmpeg) + " \"$@\"\n"
				require.NoError(t, os.WriteFile(tool, []byte(script), 0o755))
				if tc.mode == "probe_error" {
					require.NoError(t, os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\necho 'probe failed despite rc0' >&2\nexit 0\n"), 0o755))
				} else {
					require.NoError(t, os.Symlink(ffprobe, filepath.Join(dir, "ffprobe")))
				}
			}
			aggregate, err := publishLiveSessionMediaAggregate(context.Background(), tool, library, &manifest)
			require.ErrorContains(t, err, tc.want)
			require.Nil(t, aggregate)
			require.NotEmpty(t, manifest.AggregatePath)
			for _, ext := range []string{".mp4", ".subtitle.json", ".nfo", ".srt", ".ass"} {
				require.NoFileExists(t, strings.TrimSuffix(manifest.AggregatePath, ".mp4")+ext, "失败不能发布任何成品")
			}
			for path, before := range snapshots {
				require.Equal(t, before, mustReadStageFile(t, path), "失败必须保留源和原字幕")
			}
		})
	}
}

func TestLiveSessionSubtitleTimelineRejectsUnknownDuration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		duration float64
		err      error
		paths    []string
	}{
		{"probe_failure", 0, fmt.Errorf("probe failed"), []string{"source.mp4"}},
		{"zero", 0, nil, []string{"source.mp4"}},
		{"nan", math.NaN(), nil, []string{"source.mp4"}},
		{"infinite", math.Inf(1), nil, []string{"source.mp4"}},
		{"missing_path", 2, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := liveSessionMediaProbeDuration
			liveSessionMediaProbeDuration = func(context.Context, string, string) (float64, error) { return tc.duration, tc.err }
			t.Cleanup(func() { liveSessionMediaProbeDuration = old })
			got, mappings, err := mergedLiveSessionSubtitleSegments(context.Background(), "", []knowledgeSessionPayloadInput{{Metadata: &subtitle.Metadata{Segments: []subtitle.Segment{{Start: "00:00:00,000", End: "00:00:05,000", Text: "不能代替媒体时长"}}}}}, tc.paths)
			require.Error(t, err)
			require.Nil(t, got)
			require.Nil(t, mappings)
		})
	}
}

// 生产截取的短片不进入仓库；显式指定隔离 fixture 目录才运行。
func TestLiveSessionMediaCapturedHEVC(t *testing.T) {
	fixtureDir := os.Getenv("BILILIVE_MEDIA_CAPTURED_FIXTURES")
	if fixtureDir == "" {
		t.Skip("未指定已冻结的真实短片，现场样本测试未运行")
	}
	ffmpeg, ffprobe := mediaTestTools(t)
	dir := t.TempDir()
	if root := os.Getenv("BILILIVE_MEDIA_ARTIFACT_DIR"); root != "" {
		require.NoError(t, os.MkdirAll(root, 0o755))
		var err error
		dir, err = os.MkdirTemp(root, "captured-")
		require.NoError(t, err)
	}
	var inputs []string
	for _, name := range []string{"part-1.mp4", "part-2.mp4", "part-3.mp4"} {
		inputs = append(inputs, filepath.Join(fixtureDir, name))
	}
	output := filepath.Join(dir, "captured-green.mp4")
	require.NoError(t, concatLiveSessionMediaWithFFmpeg(context.Background(), ffmpeg, inputs, output))
	require.InDelta(t, 9, mediaTestDuration(t, ffprobe, output), 0.001)
	raw := func(path string, seek string, one bool) []byte {
		args := []string{"-hide_banner", "-v", "error"}
		if seek != "" {
			args = append(args, "-ss", seek)
		}
		args = append(args, "-i", path)
		if one {
			args = append(args, "-frames:v", "1")
		}
		args = append(args, "-vf", "scale=180:320", "-pix_fmt", "gray", "-f", "rawvideo", "-")
		return mediaTestCommand(t, ffmpeg, args...)
	}
	const frameBytes = 180 * 320
	var reference []byte
	for _, path := range inputs {
		part := raw(path, "", false)
		require.Len(t, part, 90*frameBytes)
		reference = append(reference, part...)
	}
	got := raw(output, "", false)
	require.Len(t, got, 270*frameBytes)
	mae := func(a, b []byte) float64 {
		require.Len(t, a, len(b))
		total := 0.0
		for i := range a {
			total += math.Abs(float64(a[i]) - float64(b[i]))
		}
		return total / float64(len(a))
	}
	maxFrameMAE := 0.0
	for i := 0; i < 270; i++ {
		n := mae(got[i*frameBytes:(i+1)*frameBytes], reference[i*frameBytes:(i+1)*frameBytes])
		maxFrameMAE = math.Max(maxFrameMAE, n)
	}
	require.Less(t, maxFrameMAE, 4.0, "每一帧必须对应独立源画面")
	for _, frame := range []int{0, 89, 90, 179, 180, 269} {
		seek := strconv.FormatFloat(float64(frame)/30, 'f', 9, 64)
		sample := raw(output, seek, true)
		require.Len(t, sample, frameBytes)
		require.Less(t, mae(sample, reference[frame*frameBytes:(frame+1)*frameBytes]), 4.0, "跨拼接点 seek 必须匹配源画面")
	}
	t.Logf("实际270帧、6次独立seek、最大逐帧MAE=%.6f/255，输出=%s", maxFrameMAE, output)
}

func TestLiveSessionMediaNormalizationPreservesCanvasAndEdges(t *testing.T) {
	ffmpeg, ffprobe := mediaTestTools(t)
	dir := t.TempDir()
	for _, tc := range []struct{ name, sourceSAR, targetSAR string }{
		{"landscape", "1/1", "1/1"},
		{"source_sar", "2/1", "1/1"},
		{"target_sar", "1/1", "2/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := mediaTestGenerate(t, ffmpeg, dir, tc.name+"-first", "360x640", "red", "setsar="+tc.targetSAR)
			wide := mediaTestGenerate(t, ffmpeg, dir, tc.name+"-wide", "640x360", "blue", "drawbox=x=0:y=0:w=32:h=ih:c=red:t=fill", "drawbox=x=608:y=0:w=32:h=ih:c=lime:t=fill", "setsar="+tc.sourceSAR, "fps=24")
			output := filepath.Join(dir, tc.name+"-green.mp4")
			require.NoError(t, concatLiveSessionMediaWithFFmpeg(context.Background(), ffmpeg, []string{first, wide}, output))
			require.InDelta(t, 4, mediaTestDuration(t, ffprobe, output), 0.001)
			data := mediaTestCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-ss", "2.5", "-i", output, "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
			require.Len(t, data, 360*640*3, "画布必须保留首源像素尺寸")
			pixel := func(x, y int) []byte { return data[(y*360+x)*3 : (y*360+x)*3+3] }
			for _, p := range [][]byte{pixel(180, 16), pixel(180, 623)} {
				for _, v := range p {
					require.Less(t, v, byte(12), "横屏上下必须补黑边")
				}
			}
			require.Greater(t, pixel(8, 320)[0], byte(200), "左红边不能被裁掉")
			require.Greater(t, pixel(351, 320)[1], byte(200), "右绿边不能被裁掉")
			require.Greater(t, pixel(180, 320)[2], byte(200), "中央蓝色画面必须保留")
			header := mediaTestCommand(t, ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=sample_aspect_ratio,r_frame_rate,nb_frames", "-of", "json", output)
			var result struct {
				Streams []struct {
					SAR    string `json:"sample_aspect_ratio"`
					FPS    string `json:"r_frame_rate"`
					Frames string `json:"nb_frames"`
				} `json:"streams"`
			}
			require.NoError(t, json.Unmarshal(header, &result))
			require.Len(t, result.Streams, 1)
			require.Equal(t, strings.ReplaceAll(tc.targetSAR, "/", ":"), result.Streams[0].SAR)
			require.Equal(t, "30/1", result.Streams[0].FPS, "每源必须统一到首源帧率")
			require.Equal(t, "120", result.Streams[0].Frames)
		})
	}
}

// v2 行为入口：真实媒体 concat、原始现场 JSON probe 与成品 PCM/压缩包时钟。
func mediaV2Dir(t *testing.T) string {
	t.Helper()
	if root := os.Getenv("BILILIVE_MEDIA_V2_ARTIFACTS"); root != "" {
		require.NoError(t, os.MkdirAll(root, 0o755))
		dir, err := os.MkdirTemp(root, strings.ReplaceAll(t.Name(), "/", "-")+"-")
		require.NoError(t, err)
		return dir
	}
	return t.TempDir()
}

func mediaV2Generate(t *testing.T, ffmpeg, dir, name, color string, fps, frames int, delay, audioDuration float64) string {
	t.Helper()
	path := filepath.Join(dir, name+".mp4")
	// 两个不同频率/包络的音频事件，作为独立的时间位置 oracle。
	expression := "aevalsrc=if(between(t\\,0.2\\,0.4)\\,0.2*(sin(2*PI*733*t)+sin(2*PI*1259*t))\\,if(between(t\\,0.61\\,0.74)\\,0.3*sin(2*PI*(500*t+700*t*t))\\,0)):s=48000:d=" + strconv.FormatFloat(audioDuration, 'f', 9, 64)
	mediaTestCommand(t, ffmpeg, "-hide_banner", "-nostdin", "-v", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("color=c=%s:s=320x180:r=%d:d=%.9f", color, fps, float64(frames)/float64(fps)), "-itsoffset", strconv.FormatFloat(delay, 'f', 9, 64), "-f", "lavfi", "-i", expression, "-c:v", "libx265", "-preset", "ultrafast", "-crf", "24", "-x265-params", "pools=1:frame-threads=1:log-level=error", "-tag:v", "hvc1", "-c:a", "aac", "-avoid_negative_ts", "disabled", path)
	return path
}

func mediaV2PCM(t *testing.T, ffmpeg, path string) []float64 {
	t.Helper()
	data := mediaTestCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-i", path, "-map", "0:a:0", "-ac", "1", "-ar", "48000", "-f", "f32le", "-")
	require.Zero(t, len(data)%4)
	samples := make([]float64, len(data)/4)
	for i := range samples {
		samples[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:])))
	}
	return samples
}

func mediaV2AudioStart(t *testing.T, ffprobe, path string) float64 {
	t.Helper()
	data := mediaTestCommand(t, ffprobe, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=start_time", "-of", "json", path)
	var value struct {
		Streams []struct {
			Start string `json:"start_time"`
		} `json:"streams"`
	}
	require.NoError(t, json.Unmarshal(data, &value))
	require.Len(t, value.Streams, 1)
	n, err := strconv.ParseFloat(value.Streams[0].Start, 64)
	require.NoError(t, err)
	return n
}

func mediaV2Correlate(t *testing.T, source, output []float64, sourceSample, outputSample, count int) (float64, int) {
	t.Helper()
	require.GreaterOrEqual(t, len(source), sourceSample+count)
	best, lag := -1.0, 0
	// 以独立源解码波形和原音轨起点定位，只搜索 ±2ms 的编码误差；不能吸收丢失的 0.1–0.25s 延迟。
	for shift := -96; shift <= 96; shift++ {
		start := outputSample + shift
		if start < 0 || start+count > len(output) {
			continue
		}
		dot, aa, bb := 0.0, 0.0, 0.0
		for i := 0; i < count; i++ {
			a, b := source[sourceSample+i], output[start+i]
			dot += a * b
			aa += a * a
			bb += b * b
		}
		if aa > 0 && bb > 0 {
			corr := dot / math.Sqrt(aa*bb)
			if corr > best {
				best, lag = corr, shift
			}
		}
	}
	require.Greater(t, best, 0.94, "原音频事件必须仍位于视频时间轴上的原位置")
	return best, lag
}

func mediaV2RMS(t *testing.T, samples []float64, start, end float64) float64 {
	t.Helper()
	a, b := int(start*48000), int(end*48000)
	require.Greater(t, b, a)
	require.GreaterOrEqual(t, len(samples), b)
	sum := 0.0
	for _, v := range samples[a:b] {
		sum += v * v
	}
	return math.Sqrt(sum / float64(b-a))
}

func TestLiveSessionMediaV2MixedFPS(t *testing.T) {
	ffmpeg, ffprobe := mediaTestTools(t)
	dir := mediaV2Dir(t)
	first := mediaV2Generate(t, ffmpeg, dir, "first", "red", 30, 60, 0, 2)
	blue := mediaV2Generate(t, ffmpeg, dir, "blue", "blue", 9, 91, 0, 91.0/9)
	green := mediaV2Generate(t, ffmpeg, dir, "green", "lime", 9, 91, 0, 91.0/9)
	for _, tc := range []struct {
		name   string
		inputs []string
		colors []int
	}{
		{"91_frames", []string{first, blue}, []int{0, 2}},
		{"multiple_fractional_segments", []string{first, blue, green, blue, green, blue, green}, []int{0, 2, 1, 2, 1, 2, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(dir, tc.name+".mp4")
			require.NoError(t, concatLiveSessionMediaWithFFmpeg(context.Background(), ffmpeg, tc.inputs, output))
			duration := 2 + float64(len(tc.inputs)-1)*91.0/9
			require.InDelta(t, duration, mediaTestDuration(t, ffprobe, output), 1.0/30+0.00001, "总误差必须限于一次整条fps量化")
			rgb := mediaTestCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-i", output, "-map", "0:v:0", "-vf", "scale=1:1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
			require.InDelta(t, duration*30, float64(len(rgb)/3), 1.001)
			transitions := []int{0}
			previous := -1
			for i := 0; i < len(rgb)/3; i++ {
				pixel := rgb[i*3 : i*3+3]
				color := 0
				if pixel[1] > pixel[color] {
					color = 1
				}
				if pixel[2] > pixel[color] {
					color = 2
				}
				if color != previous {
					if previous >= 0 {
						transitions = append(transitions, i)
					}
					previous = color
				}
			}
			require.Len(t, transitions, len(tc.colors))
			for i, frame := range transitions {
				want := 0.0
				if i > 0 {
					want = 2 + float64(i-1)*91.0/9
				}
				require.InDelta(t, want, float64(frame)/30, 1.0/30+0.00001, "第%d边界不能累计逐段fps舍入", i)
				require.Greater(t, rgb[frame*3+tc.colors[i]], byte(180))
			}
			last := len(tc.inputs) - 1
			lastOffset := 2 + float64(last-1)*91.0/9
			pcm := mediaV2PCM(t, ffmpeg, output)
			sourcePCM := mediaV2PCM(t, ffmpeg, tc.inputs[last])
			corr, lag := mediaV2Correlate(t, sourcePCM, pcm, 4800, int(math.Round(lastOffset*48000))+4800, 32000)
			require.LessOrEqual(t, int(math.Abs(float64(lag))), 2, "累计音频边界误差不得超过2个采样")
			t.Logf("物理duration=%.9f，实际帧=%d，累计边界帧=%v，末段audio corr=%.6f lag=%d", duration, len(rgb)/3, transitions, corr, lag)
		})
	}
}

func mediaV2AssertCopyClocks(t *testing.T, ffprobe, first, output string, duration float64) {
	t.Helper()
	type packet struct {
		PTS  string `json:"pts_time"`
		DTS  string `json:"dts_time"`
		Hash string `json:"data_hash"`
	}
	packets := func(path, stream string) []packet {
		data := mediaTestCommand(t, ffprobe, "-v", "error", "-select_streams", stream, "-show_packets", "-show_entries", "packet=pts_time,dts_time,data_hash", "-show_data_hash", "sha256", "-of", "json", path)
		var p struct {
			Packets []packet `json:"packets"`
		}
		require.NoError(t, json.Unmarshal(data, &p))
		return p.Packets
	}
	for _, stream := range []string{"v:0", "a:0"} {
		src, got := packets(first, stream), packets(output, stream)
		require.NotEmpty(t, src)
		require.Len(t, got, 2*len(src))
		for i, p := range got {
			s := src[i%len(src)]
			require.Equal(t, s.Hash, p.Hash)
			offset := float64(i/len(src)) * duration
			for _, times := range [][2]string{{s.PTS, p.PTS}, {s.DTS, p.DTS}} {
				a, err := strconv.ParseFloat(times[0], 64)
				require.NoError(t, err)
				b, err := strconv.ParseFloat(times[1], 64)
				require.NoError(t, err)
				require.InDelta(t, a+offset, b, 0.000002, "copy时钟必须仅增加物理媒体offset")
			}
		}
	}
}

func TestLiveSessionMediaV2AudioDelayAndCopy(t *testing.T) {
	ffmpeg, ffprobe := mediaTestTools(t)
	dir := mediaV2Dir(t)
	first := mediaV2Generate(t, ffmpeg, dir, "first", "red", 30, 60, 0.24, 0.9)
	other := mediaV2Generate(t, ffmpeg, dir, "other", "blue", 24, 48, 0.24, 0.9)
	t.Run("normalize_preserves_events_and_silence", func(t *testing.T) {
		output := filepath.Join(dir, "normalized.mp4")
		require.NoError(t, concatLiveSessionMediaWithFFmpeg(context.Background(), ffmpeg, []string{first, other}, output))
		pcm := mediaV2PCM(t, ffmpeg, output)
		require.InDelta(t, 4, float64(len(pcm))/48000, 0.022)
		for i, path := range []string{first, other} {
			src := mediaV2PCM(t, ffmpeg, path)
			delay := mediaV2AudioStart(t, ffprobe, path)
			corr, lag := mediaV2Correlate(t, src, pcm, 4800, int(math.Round((float64(i)*2+delay)*48000))+4800, 32000)
			require.Less(t, mediaV2RMS(t, pcm, float64(i)*2, float64(i)*2+0.15), 0.0001)
			require.Less(t, mediaV2RMS(t, pcm, float64(i)*2+1.4, float64(i)*2+1.95), 0.0001)
			t.Logf("源%d delay=%.6f corr=%.6f lag=%d samples", i, delay, corr, lag)
		}
	})
	t.Run("copy_preserves_packet_clocks_and_hash", func(t *testing.T) {
		output := filepath.Join(dir, "copy.mp4")
		require.NoError(t, concatLiveSessionMediaWithFFmpeg(context.Background(), ffmpeg, []string{first, first}, output))
		mediaV2AssertCopyClocks(t, ffprobe, first, output, 2)
	})
}

func TestLiveSessionMediaV2FullProbeReplay(t *testing.T) {
	fixture := os.Getenv("BILILIVE_MEDIA_FULL_PROBE_FIXTURE")
	if fixture == "" {
		t.Skip("未指定224源冻结probe")
	}
	data, err := os.ReadFile(fixture)
	require.NoError(t, err)
	digest := os.Getenv("BILILIVE_MEDIA_FULL_PROBE_FIXTURE_SHA256")
	require.NotEmpty(t, digest, "必须提供固定样本的摘要")
	require.Equal(t, digest, fmt.Sprintf("%x", sha256.Sum256(data)))
	normalizeSessions := strings.Split(os.Getenv("BILILIVE_MEDIA_FULL_PROBE_NORMALIZE_SESSIONS"), ",")
	require.Len(t, normalizeSessions, 2)
	require.NotEqual(t, normalizeSessions[0], normalizeSessions[1])
	dir := mediaV2Dir(t)
	tool := filepath.Join(dir, "ffprobe")
	require.NoError(t, os.WriteFile(tool, []byte("#!/bin/sh\nfor arg in \"$@\"; do last=$arg; done\ncat \"$last\"\n"), 0o755))
	type record struct {
		Session string          `json:"session"`
		Task    int             `json:"task"`
		Probe   json.RawMessage `json:"probe"`
	}
	groups := map[string][]liveSessionMediaContract{}
	paths := map[string][]string{}
	var results []map[string]any
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r record
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		count++
		path := filepath.Join(dir, fmt.Sprintf("%d.json", r.Task))
		require.NoError(t, os.WriteFile(path, r.Probe, 0o644))
		t.Run(fmt.Sprint(r.Task), func(t *testing.T) {
			media, e := probeLiveSessionMediaContract(context.Background(), tool, path, false)
			status := "accepted"
			if e != nil {
				status = e.Error()
			}
			results = append(results, map[string]any{"task": r.Task, "session": r.Session, "result": status})
			require.NoError(t, e)
			groups[r.Session] = append(groups[r.Session], media)
			paths[r.Session] = append(paths[r.Session], path)
		})
	}
	require.Equal(t, 224, count)
	require.Len(t, groups, 95)
	var classification []map[string]any
	normalized := map[string]bool{}
	for session, media := range groups {
		mode := "copy"
		if len(media) == 1 {
			mode = "link"
		} else if !liveSessionCopyCompatible(media) {
			mode = "normalize"
			_, err := normalizeLiveSessionMediaArgs(paths[session], media, filepath.Join(dir, "not-executed.mp4"))
			require.NoError(t, err)
		}
		classification = append(classification, map[string]any{"session": session, "mode": mode, "source_count": len(media)})
		normalized[session] = mode == "normalize"
	}
	for _, id := range normalizeSessions {
		require.True(t, normalized[id], "指定场次应进入规范化")
	}
	write := func(name string, v any) {
		b, e := json.MarshalIndent(v, "", "  ")
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o644))
	}
	write("probe-results.json", results)
	write("session-classification.json", classification)
	t.Logf("224真实JSON经最终Go probe；95场分支分类：%s", dir)
}

func TestLiveSessionMediaV2RejectsOverlappingPreroll(t *testing.T) {
	ffmpeg, _ := mediaTestTools(t)
	dir := mediaV2Dir(t)
	input := mediaV2Generate(t, ffmpeg, dir, "full-aac", "red", 30, 60, 0, 2)
	output := filepath.Join(dir, "unsafe.mp4")
	require.ErrorContains(t, concatLiveSessionMediaWithFFmpeg(context.Background(), ffmpeg, []string{input, input}, output), "Non-monotonic DTS")
	require.NoFileExists(t, output, "不能静默挪动包时钟后发布")
}

func TestLiveSessionMediaV2RepresentativeSources(t *testing.T) {
	root := os.Getenv("BILILIVE_MEDIA_REPRESENTATIVE_FIXTURES")
	if root == "" {
		t.Skip("未指定具有不同视频参数的短样")
	}
	ffmpeg, ffprobe := mediaTestTools(t)
	dir := mediaV2Dir(t)
	first, second := filepath.Join(root, "first.mp4"), filepath.Join(root, "second.mp4")
	for _, path := range []string{first, second} {
		data := mediaTestCommand(t, ffprobe, "-v", "error", "-show_streams", "-of", "json", path)
		var value struct {
			Streams []map[string]any `json:"streams"`
		}
		require.NoError(t, json.Unmarshal(data, &value))
		require.NotContains(t, value.Streams[0], "sample_aspect_ratio", "真实缺省SAR样本不能偷偷补字段")
	}
	require.Equal(t, 0.138, mediaV2AudioStart(t, ffprobe, first))
	require.Equal(t, 0.112, mediaV2AudioStart(t, ffprobe, second))
	require.InDelta(t, 91.0/9, mediaTestDuration(t, ffprobe, second), 0.000001)
	inputs := []string{first, second, first}
	output := filepath.Join(dir, "representative-green.mp4")
	require.NoError(t, concatLiveSessionMediaWithFFmpeg(context.Background(), ffmpeg, inputs, output))
	copyOutput := filepath.Join(dir, "real-compatible-copy.mp4")
	require.NoError(t, concatLiveSessionMediaWithFFmpeg(context.Background(), ffmpeg, []string{first, first}, copyOutput), "真实缺SAR同参数源必须能copy")
	mediaV2AssertCopyClocks(t, ffprobe, first, copyOutput, 3)
	total := 6 + 91.0/9
	require.InDelta(t, total, mediaTestDuration(t, ffprobe, output), 1.0/30+0.00001)
	pcm := mediaV2PCM(t, ffmpeg, output)
	offsets := []float64{0, 3, 3 + 91.0/9}
	var evidence []map[string]any
	for i, path := range inputs {
		src := mediaV2PCM(t, ffmpeg, path)
		delay := mediaV2AudioStart(t, ffprobe, path)
		// 从独立源最前2.8秒选择有能量的半秒窗口，不把静音的相关性算作成功。
		count, bestStart, bestEnergy := 24000, 4800, -1.0
		for start := 4800; start+count < len(src) && start+count < 134400; start += 4800 {
			energy := 0.0
			for _, v := range src[start : start+count] {
				energy += v * v
			}
			if energy > bestEnergy {
				bestStart, bestEnergy = start, energy
			}
		}
		require.Greater(t, bestEnergy, 0.01)
		corr, lag := mediaV2Correlate(t, src, pcm, bestStart, int(math.Round((offsets[i]+delay)*48000))+bestStart, count)
		evidence = append(evidence, map[string]any{"source": path, "offset": offsets[i], "audio_start": delay, "reference_sample": bestStart, "correlation": corr, "lag_samples": lag})
		require.Less(t, mediaV2RMS(t, pcm, offsets[i]+0.035, offsets[i]+delay-0.025), 0.0002, "音轨前置空白必须真实补静音")
	}
	tailRMS := mediaV2RMS(t, pcm, 3+8.2, 3+10.0)
	require.Less(t, tailRMS, 0.0001, "第二段两秒短尾须补静音")
	// 在输出时间轴上读帧，并与各自独立解码的源帧比较，允许一次30fps量化对应的相邻源帧。
	gray := func(path, filter string) []byte {
		return mediaTestCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-i", path, "-map", "0:v:0", "-vf", filter, "-pix_fmt", "gray", "-f", "rawvideo", "-")
	}
	// 第二段为1920×1080，独立参考明确按1440×810居中补边；不能把源强行拉伸成4:3。
	references := [][]byte{gray(first, "scale=180:135"), gray(second, "scale=1440:810,pad=1440:1080:0:135,scale=180:135"), gray(first, "scale=180:135")}
	got := gray(output, "scale=180:135")
	const pixels = 180 * 135
	maxMAE := 0.0
	for _, frame := range []int{0, 89, 91, 392, 394, 482} {
		ts := float64(frame) / 30
		source := 0
		if ts >= offsets[2] {
			source = 2
		} else if ts >= 3 {
			source = 1
		}
		rate := 30.0
		if source == 1 {
			rate = 9
		}
		local := int(math.Floor((ts - offsets[source]) * rate))
		best := math.Inf(1)
		for n := local - 1; n <= local+1; n++ {
			if n < 0 || (n+1)*pixels > len(references[source]) {
				continue
			}
			sum := 0.0
			for j, v := range references[source][n*pixels : (n+1)*pixels] {
				sum += math.Abs(float64(v) - float64(got[frame*pixels+j]))
			}
			best = math.Min(best, sum/pixels)
		}
		require.Less(t, best, 4.0, "global frame %d source %d local %d", frame, source, local)
		seekFrame := mediaTestCommand(t, ffmpeg, "-hide_banner", "-v", "error", "-ss", strconv.FormatFloat(ts, 'f', 9, 64), "-i", output, "-frames:v", "1", "-vf", "scale=180:135", "-pix_fmt", "gray", "-f", "rawvideo", "-")
		require.Equal(t, got[frame*pixels:(frame+1)*pixels], seekFrame, "独立seek须返回同一实际视频帧")
		maxMAE = math.Max(maxMAE, best)
	}
	metadata := []subtitle.Metadata{
		{Segments: []subtitle.Segment{{Start: "00:00:02,800", End: "00:00:04,000", Text: "前段裁尾"}}},
		{Segments: []subtitle.Segment{{Start: "00:00:07,500", End: "00:00:12,000", Text: "第二段裁尾"}}},
		{Segments: []subtitle.Segment{{Start: "00:00:00,200", End: "00:00:00,600", Text: "后段"}}},
	}
	before, err := json.Marshal(metadata)
	require.NoError(t, err)
	var payload []knowledgeSessionPayloadInput
	for i := range metadata {
		payload = append(payload, knowledgeSessionPayloadInput{Metadata: &metadata[i]})
	}
	merged, mappings, err := mergedLiveSessionSubtitleSegments(context.Background(), ffmpeg, payload, inputs)
	require.NoError(t, err)
	require.Equal(t, "00:00:03,000", merged[0].End)
	require.Equal(t, "00:00:13,111", merged[1].End)
	require.Equal(t, "00:00:13,311", merged[2].Start)
	require.InDelta(t, 13.111111, mappings[2]["offset"], 0.000001)
	after, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.Equal(t, before, after)
	b, err := json.MarshalIndent(map[string]any{"audio_events": evidence, "tail_rms": tailRMS, "max_video_mae": maxMAE, "source_mapping": mappings, "subtitle": merged}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "oracles.json"), b, 0o644))
	t.Logf("第二段91帧与第一段保留138ms的短样：%s；tail RMS %.9f，最大采样帧MAE %.6f", dir, tailRMS, maxMAE)
}

func TestLiveSessionMediaV2RejectsExplicitInvalidParameters(t *testing.T) {
	fixture := os.Getenv("BILILIVE_MEDIA_FULL_PROBE_FIXTURE")
	if fixture == "" {
		t.Skip("未指定冻结probe，拒绝分支未运行")
	}
	data, err := os.ReadFile(fixture)
	require.NoError(t, err)
	var row struct {
		Probe json.RawMessage `json:"probe"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.Split(string(data), "\n")[0]), &row))
	dir := mediaV2Dir(t)
	tool := filepath.Join(dir, "ffprobe")
	require.NoError(t, os.WriteFile(tool, []byte("#!/bin/sh\nfor arg in \"$@\"; do last=$arg; done\ncat \"$last\"\n"), 0o755))
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{"explicit_zero_sar", "sample_aspect_ratio", "0:1"},
		{"explicit_na_sar", "sample_aspect_ratio", "N/A"},
		{"hdr", "color_transfer", "smpte2084"},
		{"rotation", "side_data_list", []map[string]any{{"side_data_type": "Display Matrix", "rotation": 90}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var probe struct {
				Streams []map[string]any `json:"streams"`
				Format  map[string]any   `json:"format"`
			}
			require.NoError(t, json.Unmarshal(row.Probe, &probe))
			probe.Streams[0][tc.key] = tc.value
			b, err := json.Marshal(probe)
			require.NoError(t, err)
			path := filepath.Join(dir, tc.name+".json")
			require.NoError(t, os.WriteFile(path, b, 0o644))
			_, err = probeLiveSessionMediaContract(context.Background(), tool, path, false)
			require.Error(t, err, "显式无效SAR/HDR/旋转必须保持拒绝")
		})
	}
}
