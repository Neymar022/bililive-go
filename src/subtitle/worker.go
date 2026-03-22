package subtitle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
)

type ProcessRequest struct {
	SourcePath      string                    `json:"source_path"`
	OutputVideoPath string                    `json:"output_video_path"`
	OutputSRTPath   string                    `json:"output_srt_path"`
	Provider        string                    `json:"provider"`
	Language        string                    `json:"language"`
	BurnStyle       configs.SubtitleBurnStyle `json:"burn_style"`
	RecordMeta      map[string]any            `json:"record_meta,omitempty"`
}

type ProcessResponse struct {
	Segments     []Segment `json:"segments,omitempty"`
	RenderPreset string    `json:"render_preset,omitempty"`
}

type StyleLabPreviewRequest struct {
	SourcePath        string                    `json:"source_path"`
	PreviewText       string                    `json:"preview_text"`
	FrameTimeSeconds  float64                   `json:"frame_time_seconds,omitempty"`
	OutputPreviewPath string                    `json:"output_preview_path,omitempty"`
	BurnStyle         configs.SubtitleBurnStyle `json:"burn_style"`
}

type StyleLabPreviewResponse struct {
	PreviewImagePath string `json:"preview_image_path"`
	RenderPreset     string `json:"render_preset,omitempty"`
}

type StyleLabSampleRequest struct {
	SourcePath       string                    `json:"source_path"`
	SampleText       string                    `json:"sample_text"`
	StartTimeSeconds float64                   `json:"start_time_seconds,omitempty"`
	DurationSeconds  float64                   `json:"duration_seconds,omitempty"`
	OutputDir        string                    `json:"output_dir,omitempty"`
	BurnStyle        configs.SubtitleBurnStyle `json:"burn_style"`
}

type StyleLabSampleResponse struct {
	SampleVideoPath string `json:"sample_video_path"`
	SampleSRTPath   string `json:"sample_srt_path"`
	RenderPreset    string `json:"render_preset,omitempty"`
}

func ResolveRenderPreset(requestedPreset, storedPreset string, style configs.SubtitleBurnStyle) string {
	if requestedPreset != "" {
		return configs.SubtitleBurnStyle{Preset: requestedPreset}.GetEffectivePreset()
	}
	if storedPreset != "" {
		return configs.SubtitleBurnStyle{Preset: storedPreset}.GetEffectivePreset()
	}
	return style.GetEffectivePreset()
}

func ProcessFile(workerURL string, req ProcessRequest) (ProcessResponse, error) {
	req.BurnStyle.Preset = req.BurnStyle.GetEffectivePreset()
	return postToWorker[ProcessResponse](workerURL, "/api/v1/process", req)
}

func PreviewStyle(workerURL string, req StyleLabPreviewRequest) (StyleLabPreviewResponse, error) {
	req.BurnStyle.Preset = req.BurnStyle.GetEffectivePreset()
	return postToWorker[StyleLabPreviewResponse](workerURL, "/api/v1/style-lab/preview", req)
}

func GenerateStyleSample(workerURL string, req StyleLabSampleRequest) (StyleLabSampleResponse, error) {
	req.BurnStyle.Preset = req.BurnStyle.GetEffectivePreset()
	return postToWorker[StyleLabSampleResponse](workerURL, "/api/v1/style-lab/sample", req)
}

func postToWorker[T any](workerURL string, path string, req any) (T, error) {
	var response T
	body, err := json.Marshal(req)
	if err != nil {
		return response, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(workerURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return response, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Minute}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return response, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message != "" {
			var payload struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(body, &payload); err == nil && payload.Detail != "" {
				message = payload.Detail
			}
			return response, fmt.Errorf("subtitle worker returned status %d: %s", httpResp.StatusCode, message)
		}
		return response, fmt.Errorf("subtitle worker returned status %d", httpResp.StatusCode)
	}

	if err := json.NewDecoder(httpResp.Body).Decode(&response); err != nil {
		return response, err
	}

	return response, nil
}
