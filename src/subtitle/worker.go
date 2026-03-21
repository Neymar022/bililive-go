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
	var response ProcessResponse
	req.BurnStyle.Preset = req.BurnStyle.GetEffectivePreset()
	body, err := json.Marshal(req)
	if err != nil {
		return response, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(workerURL, "/")+"/api/v1/process", bytes.NewReader(body))
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
