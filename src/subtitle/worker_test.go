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
