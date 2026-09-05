package configs

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecordingModePreservesLegacyListeningAndSurvivesConfigReload(t *testing.T) {
	cfg, err := NewConfigWithBytes([]byte(`live_rooms:
- https://live.bilibili.com/100
- url: https://live.bilibili.com/200
  is_listening: false
- url: https://live.bilibili.com/300
  recording_mode: once
`))
	require.NoError(t, err)
	require.Equal(t, RecordingModeContinuous, cfg.LiveRooms[0].EffectiveRecordingMode())
	require.True(t, cfg.LiveRooms[0].IsListening)
	require.Equal(t, RecordingModeContinuous, cfg.LiveRooms[1].EffectiveRecordingMode())
	require.False(t, cfg.LiveRooms[1].IsListening)
	require.Equal(t, RecordingModeOnce, cfg.LiveRooms[2].EffectiveRecordingMode())
	cfg.File = filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, cfg.Marshal())
	reloaded, err := NewConfigWithFile(cfg.File)
	require.NoError(t, err)
	require.Equal(t, RecordingModeOnce, reloaded.LiveRooms[2].EffectiveRecordingMode())
	require.False(t, reloaded.LiveRooms[1].IsListening)
	var room LiveRoom
	require.NoError(t, json.Unmarshal([]byte(`{"url":"https://live.bilibili.com/400","is_listening":true}`), &room))
	require.Equal(t, RecordingModeContinuous, room.EffectiveRecordingMode())
}

func TestRecordingModeRejectsUnknownValuesBeforeActivation(t *testing.T) {
	cfg := NewConfig()
	cfg.OutPutPath = t.TempDir()
	for _, mode := range []RecordingMode{"ones", "ONCE", " once ", "42"} {
		cfg.LiveRooms = []LiveRoom{{Url: "https://live.bilibili.com/100", RecordingMode: mode}}
		require.ErrorContains(t, cfg.Verify(), "recording_mode")
	}
	for _, mode := range []RecordingMode{"", RecordingModeOnce, RecordingModeContinuous} {
		cfg.LiveRooms = []LiveRoom{{Url: "https://live.bilibili.com/100", RecordingMode: mode}}
		require.NoError(t, cfg.Verify())
	}
}
