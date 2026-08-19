package subtitle

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMediaDisplayTitleHidesChronologicalEpisodeIdentity(t *testing.T) {
	path := "/video/陶-琛霸/Season 01/陶-琛霸.S01E1673386692282296.2026-08-18 - 大班妈妈都在听.mp4"

	assert.Equal(t, "2026-08-18 - 大班妈妈都在听", MediaDisplayTitle(path))
}

func TestMediaDisplayTitleKeepsAggregateLabel(t *testing.T) {
	path := "/video/建筑师 linkai/Season 01/建筑师 linkai.S01E1673846015111776.2026-08-18 - 设计师还在加班画图吗？进来看看！ [同场聚合].mp4"

	assert.Equal(t, "2026-08-18 - 设计师还在加班画图吗？进来看看！ [同场聚合]", MediaDisplayTitle(path))
}

func TestMediaDisplayTitleUsesNormalizedRecordingTime(t *testing.T) {
	path := "/recordings/主播 - 2026-08-18 07-42-16 - 房间标题.flv"

	assert.Equal(t, "2026-08-18 07-42-16 - 房间标题", MediaDisplayTitle(path))
}

func TestMediaDisplayTitleKeepsUnmatchedBasename(t *testing.T) {
	assert.Equal(t, "cover.jpg", MediaDisplayTitle("/video/cover.jpg"))
}

func TestMediaDisplayTitleDoesNotRewriteLegacyShortEpisodeNumbers(t *testing.T) {
	path := "/video/主播/Season 01/主播.S01E0047.2026-05-27 - 房间标题.mp4"

	assert.Equal(t, filepath.Base(path), MediaDisplayTitle(path))
}

func TestMediaDisplayTitleKeepsDotsInsideRoomTitle(t *testing.T) {
	path := "/video/主播/Season 01/主播.S01E1673386692282296.2026-08-18 - Go1.25 与 AI.mp4"

	assert.Equal(t, "2026-08-18 - Go1.25 与 AI", MediaDisplayTitle(path))
}

func TestEpisodeNFOSeparatesDisplayTitleFromChronologicalIdentity(t *testing.T) {
	recordedAt := time.Date(2026, 8, 18, 7, 42, 16, 0, time.FixedZone("UTC+8", 8*60*60))
	episode := episodeNumberForRecordedAt(recordedAt)
	nfo := buildEpisodeNFO("主播", episode, recordedAt, "房间标题", "抖音")

	assert.Contains(t, nfo, "<title>2026-08-18 - 房间标题</title>")
	assert.Contains(t, nfo, "<showtitle>主播</showtitle>")
	assert.Contains(t, nfo, "<episode>"+fmt.Sprint(episode)+"</episode>")
	assert.NotContains(t, nfo, "<title>"+fmt.Sprint(episode))
}

func TestRecordDisplayTitleUsesRoomMetadataWithoutChangingIdentityPath(t *testing.T) {
	recordedAt := time.Date(2026, 8, 18, 7, 42, 16, 0, time.FixedZone("UTC+8", 8*60*60))
	path := "/video/主播/Season 01/主播.S01E1673386692282296.2026-08-18 - 旧标题.mp4"

	assert.Equal(t, "2026-08-18 - 新房间标题", RecordDisplayTitle(path, "新房间标题", recordedAt))
	assert.Contains(t, path, "1673386692282296")
}
