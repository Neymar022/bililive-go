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
		"主播.S01E1685894400000000.2026-09-05 - 房间标题 [同场聚合].mp4",
	)
	inputs := []knowledgeSessionPayloadInput{{
		LibraryPath: aggregatePath,
		Metadata: &subtitle.Metadata{RecordMeta: map[string]interface{}{
			"start_time": time.Date(2026, 9, 5, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)).Format(time.RFC3339),
		}},
	}}

	require.NoError(t, writeLiveSessionEpisodeNFO(aggregatePath, inputs))
	nfoPath := aggregatePath[:len(aggregatePath)-len(filepath.Ext(aggregatePath))] + ".nfo"
	content, err := os.ReadFile(nfoPath)
	require.NoError(t, err)

	assert.Contains(t, string(content), "<title>2026-09-05 - 房间标题 [同场聚合]</title>")
	assert.Contains(t, string(content), "<sorttitle>主播 - 2026-09-05 10-00-00</sorttitle>")
	assert.Contains(t, string(content), "<episode>1</episode>")
	assert.Contains(t, string(content), `<uniqueid type="bililive-recorded-at" default="false">1685894400000000</uniqueid>`)
	assert.NotContains(t, string(content), "<sorttitle>主播.S01E1685894400000000")
}
