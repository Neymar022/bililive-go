package stages

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveSessionEpisodeNFOSeparatesDisplaySortTitleFromRecordedAtIdentity(t *testing.T) {
	seasonDir := filepath.Join(t.TempDir(), "主播", "Season 01")
	require.NoError(t, os.MkdirAll(seasonDir, 0o755))
	aggregatePath := filepath.Join(
		seasonDir,
		"主播.S01E1673386692282296.2026-08-18 - 房间标题 [同场聚合].mp4",
	)
	inputs := []knowledgeSessionPayloadInput{{
		LibraryPath: aggregatePath,
		Metadata: &subtitle.Metadata{RecordMeta: map[string]interface{}{
			"start_time": time.Date(2026, 8, 18, 7, 42, 16, 0, time.FixedZone("UTC+8", 8*60*60)).Format(time.RFC3339),
		}},
	}}

	require.NoError(t, writeLiveSessionEpisodeNFO(aggregatePath, inputs))
	nfoPath := aggregatePath[:len(aggregatePath)-len(filepath.Ext(aggregatePath))] + ".nfo"
	content, err := os.ReadFile(nfoPath)
	require.NoError(t, err)

	assert.Contains(t, string(content), "<title>2026-08-18 - 房间标题 [同场聚合]</title>")
	assert.Contains(t, string(content), "<sorttitle>主播 - 2026-08-18 07-42-16</sorttitle>")
	assert.Contains(t, string(content), "<episode>1673386692282296</episode>")
	assert.NotContains(t, string(content), "<sorttitle>主播.S01E1673386692282296")
}
