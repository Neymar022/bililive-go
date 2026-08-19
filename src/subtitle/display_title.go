package subtitle

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var chronologicalDisplayFilenamePattern = regexp.MustCompile(
	`^(?P<alias_name>.+?)\.S\d{2}E\d{10,}(?:-S\d{2}E\d{10,})?\.(?P<recorded_date>\d{4}-\d{2}-\d{2}) - (?P<title>.+)$`,
)

// MediaDisplayTitle 返回面向用户的录屏标题，并保留原始路径用于身份、关联和文件操作。
func MediaDisplayTitle(path string) string {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	if match := chronologicalDisplayFilenamePattern.FindStringSubmatch(stem); match != nil {
		date := match[chronologicalDisplayFilenamePattern.SubexpIndex("recorded_date")]
		title := strings.TrimSpace(match[chronologicalDisplayFilenamePattern.SubexpIndex("title")])
		if date != "" && title != "" {
			return date + " - " + title
		}
	}

	if match := normalizedFilenamePattern.FindStringSubmatch(stem); match != nil {
		recordedAt := match[normalizedFilenamePattern.SubexpIndex("recorded_at")]
		title := strings.TrimSpace(match[normalizedFilenamePattern.SubexpIndex("title")])
		if recordedAt != "" && title != "" {
			return recordedAt + " - " + title
		}
	}

	return base
}

// RecordDisplayTitle 优先使用权威录制元数据，旧版或不完整 sidecar 则回退到路径标题。
func RecordDisplayTitle(path, roomName string, recordedAt time.Time) string {
	roomName = strings.TrimSpace(roomName)
	if roomName != "" && !recordedAt.IsZero() {
		return recordedAt.In(mediaLibraryLocation).Format("2006-01-02") + " - " + roomName
	}
	return MediaDisplayTitle(path)
}
