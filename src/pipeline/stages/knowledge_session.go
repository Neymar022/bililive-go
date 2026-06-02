package stages

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/pipeline"
)

type knowledgeSessionCandidate struct {
	RecordInfo      pipeline.RecordInfo
	LibraryPath     string
	DurationSeconds float64
}

func knowledgeSessionKey(candidate knowledgeSessionCandidate) string {
	if sessionID := strings.TrimSpace(candidate.RecordInfo.LiveSessionID); sessionID != "" {
		return "live-session:" + sessionID
	}

	host := normalizeKnowledgeSessionText(candidate.RecordInfo.HostName)
	title := normalizeKnowledgeSessionTitle(candidate.RecordInfo.RoomName, candidate.LibraryPath)
	liveID := strings.TrimSpace(string(candidate.RecordInfo.LiveID))
	platform := normalizeKnowledgeSessionText(candidate.RecordInfo.Platform)
	date := candidate.RecordInfo.StartTime.Format("2006-01-02")
	if host == "" || title == "" || candidate.RecordInfo.StartTime.IsZero() {
		return ""
	}
	return fmt.Sprintf("fallback:%s:%s:%s:%s:%s", platform, liveID, host, title, date)
}

func sameKnowledgeLiveSession(first, second knowledgeSessionCandidate, quietWindow time.Duration) bool {
	firstSessionID := strings.TrimSpace(first.RecordInfo.LiveSessionID)
	secondSessionID := strings.TrimSpace(second.RecordInfo.LiveSessionID)
	if firstSessionID != "" || secondSessionID != "" {
		return firstSessionID != "" && firstSessionID == secondSessionID
	}

	firstKey := knowledgeSessionKey(first)
	if firstKey == "" || firstKey != knowledgeSessionKey(second) {
		return false
	}
	if quietWindow <= 0 {
		return true
	}

	gap := second.RecordInfo.StartTime.Sub(first.endTime())
	if gap < 0 {
		gap = first.RecordInfo.StartTime.Sub(second.endTime())
	}
	return gap <= quietWindow
}

func shouldSkipStandaloneKnowledgeArtifact(durationSeconds float64, hasSession bool, minDuration time.Duration) bool {
	if hasSession || minDuration <= 0 {
		return false
	}
	return durationSeconds > 0 && durationSeconds <= minDuration.Seconds()
}

func (candidate knowledgeSessionCandidate) endTime() time.Time {
	if candidate.RecordInfo.StartTime.IsZero() || candidate.DurationSeconds <= 0 {
		return candidate.RecordInfo.StartTime
	}
	return candidate.RecordInfo.StartTime.Add(time.Duration(candidate.DurationSeconds * float64(time.Second)))
}

func normalizeKnowledgeSessionText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func normalizeKnowledgeSessionTitle(roomName string, libraryPath string) string {
	if title := normalizeKnowledgeSessionText(roomName); title != "" {
		return title
	}
	base := strings.TrimSuffix(filepath.Base(libraryPath), filepath.Ext(libraryPath))
	return normalizeKnowledgeSessionText(stripKnowledgeEpisodePrefix(base))
}

var knowledgeEpisodeTitlePattern = regexp.MustCompile(`^.+?\.S\d+E\d+\.\d{4}-\d{2}-\d{2}\s+-\s+`)

func stripKnowledgeEpisodePrefix(value string) string {
	return knowledgeEpisodeTitlePattern.ReplaceAllString(value, "")
}
