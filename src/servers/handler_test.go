package servers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bililive-go/bililive-go/src/configs"
)

func TestGetSoopLiveAuthConfigDoesNotExposeSavedPassword(t *testing.T) {
	cfg := configs.NewConfig()
	cfg.SoopLiveAuth.Username = "tester"
	cfg.SoopLiveAuth.Password = "secret"
	configs.SetCurrentConfig(cfg)

	recorder := httptest.NewRecorder()
	getSoopLiveAuthConfig(recorder, nil)

	assert.Equal(t, 200, recorder.Code)

	var resp commonResp
	err := json.Unmarshal(recorder.Body.Bytes(), &resp)
	assert.NoError(t, err)

	data, ok := resp.Data.(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "tester", data["username"])
	assert.Equal(t, true, data["has_saved_credentials"])
	_, exists := data["password"]
	assert.False(t, exists)
}

func TestGetFileInfoReturnsCleanDisplayNameWithoutChangingRawName(t *testing.T) {
	root := t.TempDir()
	name := "陶-琛霸.S01E1673386692282296.2026-08-18 - 大班妈妈都在听.mp4"
	require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("video"), 0o644))
	cfg := configs.NewConfig()
	cfg.OutPutPath = root
	configs.SetCurrentConfig(cfg)

	request := mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/api/files/", nil), map[string]string{"path": ""})
	recorder := httptest.NewRecorder()
	getFileInfo(recorder, request)

	var response struct {
		Files []struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response.Files, 1)
	assert.Equal(t, name, response.Files[0].Name)
	assert.Equal(t, "2026-08-18 - 大班妈妈都在听", response.Files[0].DisplayName)
}
