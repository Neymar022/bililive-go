package subtitle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessFileIncludesWorkerDetailOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"detail":"model download failed"}`))
	}))
	t.Cleanup(server.Close)

	_, err := ProcessFile(server.URL, ProcessRequest{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Contains(t, err.Error(), "model download failed")
}

func TestProcessFileSendsEffectiveRenderPreset(t *testing.T) {
	var request ProcessRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		require.NoError(t, json.NewDecoder(req.Body).Decode(&request))
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(ProcessResponse{}))
	}))
	t.Cleanup(server.Close)

	_, err := ProcessFile(server.URL, ProcessRequest{
		BurnStyle: configs.SubtitleBurnStyle{
			Preset: "bottom_center",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "vizard_classic_cn", request.BurnStyle.Preset)
}

func TestPreviewStyleSendsPreviewPayload(t *testing.T) {
	var request StyleLabPreviewRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/api/v1/style-lab/preview", req.URL.Path)
		require.NoError(t, json.NewDecoder(req.Body).Decode(&request))
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(StyleLabPreviewResponse{
			PreviewImagePath: "/tmp/preview.png",
			RenderPreset:     "vizard_classic_cn",
		}))
	}))
	t.Cleanup(server.Close)

	response, err := PreviewStyle(server.URL, StyleLabPreviewRequest{
		SourcePath:  "/tmp/source.mp4",
		PreviewText: "测试文案",
		BurnStyle: configs.SubtitleBurnStyle{
			Preset: "bottom_center",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "vizard_classic_cn", request.BurnStyle.Preset)
	assert.Equal(t, "测试文案", request.PreviewText)
	assert.Equal(t, "/tmp/preview.png", response.PreviewImagePath)
}

func TestGenerateStyleSampleSendsSamplePayload(t *testing.T) {
	var request StyleLabSampleRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		require.Equal(t, "/api/v1/style-lab/sample", req.URL.Path)
		require.NoError(t, json.NewDecoder(req.Body).Decode(&request))
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(StyleLabSampleResponse{
			SampleVideoPath: "/tmp/sample.burned.mp4",
			SampleSRTPath:   "/tmp/sample.srt",
			RenderPreset:    "vizard_classic_cn",
		}))
	}))
	t.Cleanup(server.Close)

	response, err := GenerateStyleSample(server.URL, StyleLabSampleRequest{
		SourcePath:      "/tmp/source.mp4",
		SampleText:      "30 秒样片",
		DurationSeconds: 45,
		BurnStyle: configs.SubtitleBurnStyle{
			Preset: "bottom_center",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "vizard_classic_cn", request.BurnStyle.Preset)
	assert.Equal(t, "30 秒样片", request.SampleText)
	assert.Equal(t, 45.0, request.DurationSeconds)
	assert.Equal(t, "/tmp/sample.burned.mp4", response.SampleVideoPath)
	assert.Equal(t, "/tmp/sample.srt", response.SampleSRTPath)
}
