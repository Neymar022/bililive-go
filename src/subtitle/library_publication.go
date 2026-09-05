package subtitle

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LockLibraryPublication 将普通和整场发布串行化到同一季度目录。
// 调用方须在生成 NFO 前加锁，直到媒体与 sidecar 提交或回滚后释放。
func LockLibraryPublication(libraryPath string) func() {
	return lockLibraryPath(&libraryEpisodePublishLocks, filepath.Dir(libraryPath))
}

// ValidatePublishedLibraryEpisode 在重用成品前核对媒体、身份及合集集号。
func ValidatePublishedLibraryEpisode(path string) error {
	_, recordedAt, err := publishedEpisodeMetadata(path, episodeIdentityFromLibraryPath(path))
	if err != nil {
		return err
	}
	_, err = compatibleEpisodeOrdinalForRecordedAt(filepath.Dir(path), recordedAt, path)
	return err
}

// BuildLibraryEpisodeNFO 使用实际公开媒体集合分配集号，独立保留录制身份。
// libraryPath 必须为最终公开路径，不能使用暂存路径计算公开集号。
func BuildLibraryEpisodeNFO(libraryPath string, recordedAt time.Time, platform string, replacedPaths ...string) (string, error) {
	meta, ok := parseLibraryEpisodeFilename(libraryPath)
	if !ok || recordedAt.IsZero() {
		return "", fmt.Errorf("no reliable episode identity or recording start time: %s", libraryPath)
	}
	identity := episodeIdentityFromLibraryPath(libraryPath)
	if identity <= maxUGREENEpisodeOrdinal {
		identity = episodeIdentityForRecordedAt(recordedAt)
	}
	if identity <= 0 || identity > maxSafeEpisodeIdentity {
		return "", fmt.Errorf("invalid recording-time identity: %s", libraryPath)
	}
	base := episodeIdentityForRecordedAt(recordedAt)
	if identity < base || identity >= base+chronologicalEpisodeIdentityBase {
		return "", fmt.Errorf("recording time and filename identity mismatch: %s", libraryPath)
	}
	ordinal, err := compatibleEpisodeOrdinalForRecordedAt(filepath.Dir(libraryPath), recordedAt, append([]string{libraryPath}, replacedPaths...)...)
	if err != nil {
		return "", err
	}
	return buildEpisodeNFO(meta.aliasName, ordinal, identity, recordedAt.In(mediaLibraryLocation), meta.title, platform), nil
}

func publishedEpisodeMetadata(mediaPath string, identity int64) (int64, time.Time, error) {
	media, err := os.Open(mediaPath)
	if err != nil {
		return 0, time.Time{}, err
	}
	info, err := media.Stat()
	_ = media.Close()
	if err != nil {
		return 0, time.Time{}, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return 0, time.Time{}, fmt.Errorf("published media is not a nonempty regular file: %s", mediaPath)
	}
	content, err := os.ReadFile(sidecarStem(mediaPath) + ".nfo")
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("published episode NFO ordinal migration required: %s: %w", mediaPath, err)
	}
	var nfo struct {
		XMLName   xml.Name `xml:"episodedetails"`
		Episode   int64    `xml:"episode"`
		UniqueIDs []struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"uniqueid"`
	}
	if err := xml.Unmarshal(content, &nfo); err != nil || nfo.Episode <= 0 || nfo.Episode > maxUGREENEpisodeOrdinal {
		return 0, time.Time{}, fmt.Errorf("published episode requires NFO ordinal migration: %s", mediaPath)
	}
	recordedAt, exact := recordedAtForEpisodeIdentity(identity)
	if !exact {
		recordedAt, exact = episodeRecordedAtFromSidecars(sidecarStem(mediaPath))
	}
	if !exact {
		return 0, time.Time{}, fmt.Errorf("no reliable recording start time: %s", mediaPath)
	}
	var uniqueIdentity int64
	var count int
	for _, value := range nfo.UniqueIDs {
		if value.Type == "bililive-recorded-at" {
			count++
			uniqueIdentity, err = strconv.ParseInt(strings.TrimSpace(value.Value), 10, 64)
			if err != nil {
				return 0, time.Time{}, fmt.Errorf("invalid NFO identity: %s", mediaPath)
			}
		}
	}
	base := episodeIdentityForRecordedAt(recordedAt)
	if count != 1 || uniqueIdentity < base || uniqueIdentity >= base+chronologicalEpisodeIdentityBase || (identity > maxUGREENEpisodeOrdinal && uniqueIdentity != identity) {
		return 0, time.Time{}, fmt.Errorf("published NFO identity mismatch: %s", mediaPath)
	}
	return nfo.Episode, recordedAt, nil
}
