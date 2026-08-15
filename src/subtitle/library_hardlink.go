package subtitle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bililive-go/bililive-go/src/tools"
	"github.com/sirupsen/logrus"
)

// normalizedFilenamePattern matches bililive-go's normalized output filename:
//
//	<alias_name> - YYYY-MM-DD HH-MM-SS - <title>
//
// This mirrors NORMALIZED_PATTERN in bililive_media_organizer.py.
var normalizedFilenamePattern = regexp.MustCompile(
	`^(?P<alias_name>.+?) - (?P<recorded_at>\d{4}-\d{2}-\d{2} \d{2}-\d{2}-\d{2}) - (?P<title>.+)$`,
)

var libraryEpisodeSlotPattern = regexp.MustCompile(`\.S01E(\d+)(?:-S\d{2}E(\d+))?\.`)
var libraryEpisodeFilenamePattern = regexp.MustCompile(
	`^(?P<alias_name>.+?)\.S\d{2}E\d+(?:-S\d{2}E\d+)?\.(?P<recorded_date>\d{4}-\d{2}-\d{2}) - (?P<title>.+)$`,
)

// invalidFilenameChars mirrors INVALID_FILENAME_CHARS in bililive_media_organizer.py.
var invalidFilenameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`)

var extractCoverTo = tools.ExtractCoverTo
var libraryHardlinkLink = os.Link
var librarySidecarLink = os.Link
var libraryEpisodePublishLocks keyedPathLocks
var libraryShowPosterLocks keyedPathLocks
var mediaLibraryLocation = time.FixedZone("UTC+8", 8*60*60)

const (
	chronologicalEpisodeIdentityBase int64 = 8
	chronologicalEpisodeEpochUnix    int64 = 1_577_836_800 // 2020-01-01T00:00:00Z
	maxSafeEpisodeIdentity           int64 = 9_007_199_254_740_991
)

type keyedPathLockEntry struct {
	mutex sync.Mutex
	refs  int
}

type keyedPathLocks struct {
	mutex   sync.Mutex
	entries map[string]*keyedPathLockEntry
}

type libraryPublishedHardlink struct {
	staged      string
	target      string
	size        int64
	modTime     time.Time
	digest      [sha256.Size]byte
	checkDigest bool
}

type libraryFileSnapshot struct {
	path    string
	existed bool
	content []byte
	mode    os.FileMode
	info    os.FileInfo
}

type libraryShowSnapshotManifestEntry struct {
	Path    string      `json:"path"`
	Existed bool        `json:"existed"`
	Mode    os.FileMode `json:"mode"`
	Backup  string      `json:"backup,omitempty"`
}

// sanitizeComponent replaces invalid filename characters with spaces, collapses
// multiple whitespace, and trims leading/trailing spaces and dots.
// Mirrors sanitize_component() in bililive_media_organizer.py.
func sanitizeComponent(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		return r
	}, s)
	s = invalidFilenameChars.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.Trim(s, " .")
}

// truncateUTF8 truncates s so its UTF-8 byte length does not exceed maxBytes,
// keeping complete runes. Mirrors truncate_utf8() in bililive_media_organizer.py.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	b := []byte(s)
	truncated := b[:maxBytes]
	// Walk back until we have a valid UTF-8 boundary.
	for !utf8.Valid(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return string(truncated)
}

// buildEpisodeFilename constructs the Plex-style episode filename.
// 格式：<aliasName>.S01E<identity>.<YYYY-MM-DD> - <title><extension>。
// Mirrors build_episode_filename() in bililive_tv_library_builder.py.
func buildEpisodeFilename(aliasName string, episodeNumber int64, recordedAt time.Time, title, extension string) string {
	aliasName = sanitizeComponent(aliasName)
	if aliasName == "" {
		aliasName = "未分类主播"
	}
	title = sanitizeComponent(title)
	if title == "" {
		title = "未命名直播"
	}
	displayTitle := fmt.Sprintf("%s - %s", recordedAt.Format("2006-01-02"), title)
	prefix := fmt.Sprintf("%s.S01E%04d.", aliasName, episodeNumber)

	// Respect 255-byte filename limit (like the Python builder).
	maxBytes := 255 - len([]byte(extension))
	prefixBytes := len([]byte(prefix))
	if prefixBytes >= maxBytes {
		aliasName = truncateUTF8(aliasName, max(24, maxBytes/3))
		prefix = fmt.Sprintf("%s.S01E%04d.", aliasName, episodeNumber)
		prefixBytes = len([]byte(prefix))
	}
	displayTitle = truncateUTF8(displayTitle, max(1, maxBytes-prefixBytes))
	return prefix + displayTitle + extension
}

func buildEpisodeNFO(aliasName string, episodeNumber int64, recordedAt time.Time, title, platform string) string {
	aliasName = sanitizeComponent(aliasName)
	if aliasName == "" {
		aliasName = "未分类主播"
	}
	title = sanitizeComponent(title)
	if title == "" {
		title = "未命名直播"
	}
	platform = sanitizeComponent(platform)
	if platform == "" {
		platform = "bililive-go"
	}

	aired := recordedAt.Format("2006-01-02")
	recordedAtText := recordedAt.Format("2006-01-02 15-04-05")
	displayTitle := fmt.Sprintf("%s - %s", aired, title)
	sortTitle := fmt.Sprintf("%s - %s", aliasName, recordedAtText)
	plot := fmt.Sprintf("%s | 主播: %s | 标题: %s | 录制时间: %s", platform, aliasName, title, recordedAtText)

	return strings.Join([]string{
		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`,
		"<episodedetails>",
		fmt.Sprintf("  <title>%s</title>", xmlEscape(displayTitle)),
		fmt.Sprintf("  <showtitle>%s</showtitle>", xmlEscape(aliasName)),
		fmt.Sprintf("  <sorttitle>%s</sorttitle>", xmlEscape(sortTitle)),
		"  <season>1</season>",
		fmt.Sprintf("  <episode>%d</episode>", episodeNumber),
		fmt.Sprintf("  <plot>%s</plot>", xmlEscape(plot)),
		fmt.Sprintf("  <studio>%s</studio>", xmlEscape(platform)),
		"  <genre>直播录屏</genre>",
		"  <tag>直播录屏</tag>",
		fmt.Sprintf("  <aired>%s</aired>", aired),
		fmt.Sprintf("  <dateadded>%s</dateadded>", recordedAt.Format("2006-01-02 15:04:05")),
		"</episodedetails>",
		"",
	}, "\n")
}

func buildShowNFO(aliasName string, recordedAt time.Time, platform string) string {
	aliasName = sanitizeComponent(aliasName)
	if aliasName == "" {
		aliasName = "未分类主播"
	}
	platform = sanitizeComponent(platform)
	if platform == "" {
		platform = "bililive-go"
	}

	aired := recordedAt.Format("2006-01-02")
	plot := fmt.Sprintf("%s 的直播录屏剧集库。来源平台: %s。", aliasName, platform)

	return strings.Join([]string{
		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`,
		"<tvshow>",
		fmt.Sprintf("  <title>%s</title>", xmlEscape(aliasName)),
		fmt.Sprintf("  <showtitle>%s</showtitle>", xmlEscape(aliasName)),
		fmt.Sprintf("  <sorttitle>%s</sorttitle>", xmlEscape(aliasName)),
		fmt.Sprintf("  <year>%d</year>", recordedAt.Year()),
		fmt.Sprintf("  <plot>%s</plot>", xmlEscape(plot)),
		fmt.Sprintf("  <studio>%s</studio>", xmlEscape(platform)),
		"  <genre>直播录屏</genre>",
		"  <tag>直播录屏</tag>",
		`  <thumb aspect="poster">poster.jpg</thumb>`,
		fmt.Sprintf("  <premiered>%s</premiered>", aired),
		fmt.Sprintf("  <dateadded>%s</dateadded>", recordedAt.Format("2006-01-02 15:04:05")),
		"</tvshow>",
		"",
	}, "\n")
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}

func episodeNumberForRecordedAt(recordedAt time.Time) int64 {
	microseconds := recordedAt.UnixMicro() - chronologicalEpisodeEpochUnix*1_000_000
	if recordedAt.IsZero() || microseconds <= 0 {
		return 0
	}
	if microseconds > maxSafeEpisodeIdentity/chronologicalEpisodeIdentityBase {
		return -1
	}
	identity := microseconds * chronologicalEpisodeIdentityBase
	return identity
}

func episodeNumberReserved(dir string, target int64) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := libraryEpisodeSlotPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		first, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		last := first
		if len(match) > 2 && match[2] != "" {
			if parsed, parseErr := strconv.ParseInt(match[2], 10, 64); parseErr == nil && parsed >= first {
				last = parsed
			}
		}
		if target >= first && target <= last {
			return true, nil
		}
	}
	return false, nil
}

func episodeNumberFromLibraryPath(path string) int64 {
	match := libraryEpisodeSlotPattern.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return 0
	}
	episodeNumber, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0
	}
	return episodeNumber
}

// parseSourceFileMeta extracts aliasName, recordedAt, and title from a source
// filename following the bililive-go normalized convention:
//
//	<alias_name> - YYYY-MM-DD HH-MM-SS - <title>
//
// Falls back to hostName/startTime from RecordInfo when parsing fails.
type sourceFileMeta struct {
	aliasName  string
	recordedAt time.Time
	title      string
}

func parseSourceFilename(sourcePath, fallbackHost string, fallbackTime time.Time) sourceFileMeta {
	stem := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	m := normalizedFilenamePattern.FindStringSubmatch(stem)
	if m == nil {
		return sourceFileMeta{
			aliasName:  sanitizeComponent(fallbackHost),
			recordedAt: fallbackTime,
			title:      "未命名直播",
		}
	}
	aliasName := sanitizeComponent(m[normalizedFilenamePattern.SubexpIndex("alias_name")])
	recordedAtStr := m[normalizedFilenamePattern.SubexpIndex("recorded_at")]
	title := sanitizeComponent(m[normalizedFilenamePattern.SubexpIndex("title")])

	recordedAt := fallbackTime
	if recordedAt.IsZero() {
		parsed, err := time.ParseInLocation("2006-01-02 15-04-05", recordedAtStr, mediaLibraryLocation)
		if err == nil {
			recordedAt = parsed
		}
	}
	if aliasName == "" {
		aliasName = sanitizeComponent(fallbackHost)
	}
	if aliasName == "" {
		aliasName = "未分类主播"
	}
	if title == "" {
		title = "未命名直播"
	}
	return sourceFileMeta{aliasName: aliasName, recordedAt: recordedAt, title: title}
}

func parseLibraryEpisodeFilename(libraryPath string) (sourceFileMeta, bool) {
	stem := strings.TrimSuffix(filepath.Base(libraryPath), filepath.Ext(libraryPath))
	m := libraryEpisodeFilenamePattern.FindStringSubmatch(stem)
	if m == nil {
		return sourceFileMeta{}, false
	}
	aliasName := sanitizeComponent(m[libraryEpisodeFilenamePattern.SubexpIndex("alias_name")])
	title := sanitizeComponent(m[libraryEpisodeFilenamePattern.SubexpIndex("title")])
	recordedDate := m[libraryEpisodeFilenamePattern.SubexpIndex("recorded_date")]
	recordedAt, err := time.ParseInLocation("2006-01-02", recordedDate, time.Local)
	if err != nil {
		recordedAt = time.Now()
	}
	if aliasName == "" {
		aliasName = "未分类主播"
	}
	if title == "" {
		title = "未命名直播"
	}
	return sourceFileMeta{aliasName: aliasName, recordedAt: recordedAt, title: title}, true
}

// findExistingHardlinkInDir scans dir for any mp4 file that shares the same
// inode as sourcePath (i.e., is a hardlink to it). Returns the path if found,
// empty string otherwise.
func findExistingHardlinkInDir(sourcePath, dir string) (string, error) {
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory doesn't exist yet — that's fine.
		return "", nil
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		candidatePath := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if os.SameFile(sourceInfo, info) {
			return candidatePath, nil
		}
	}
	return "", nil
}

func findExistingMetadataOutputInDir(sourcePath, dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".subtitle.json") {
			continue
		}
		metadataPath := filepath.Join(dir, e.Name())
		metadata, err := LoadMetadata(metadataPath)
		if err != nil || metadata.SourcePath != sourcePath {
			continue
		}
		outputPath := metadata.OutputPath
		if outputPath == "" {
			outputPath = strings.TrimSuffix(metadataPath, ".subtitle.json") + filepath.Ext(sourcePath)
		}
		if _, err := os.Stat(outputPath); err == nil {
			return outputPath
		}
	}
	return ""
}

func sidecarStem(path string) string {
	return strings.TrimSuffix(path, filepath.Ext(path))
}

func lockLibraryPath(locks *keyedPathLocks, path string) func() {
	key, err := filepath.Abs(path)
	if err != nil {
		key = filepath.Clean(path)
	}

	locks.mutex.Lock()
	if locks.entries == nil {
		locks.entries = make(map[string]*keyedPathLockEntry)
	}
	entry := locks.entries[key]
	if entry == nil {
		entry = &keyedPathLockEntry{}
		locks.entries[key] = entry
	}
	entry.refs++
	locks.mutex.Unlock()

	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		locks.mutex.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(locks.entries, key)
		}
		locks.mutex.Unlock()
	}
}

func ensureTextFile(path, text string) error {
	if current, err := os.ReadFile(path); err == nil && string(current) == text {
		return nil
	}
	return writeFileAtomically(path, []byte(text), 0o644)
}

func writeFileAtomically(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func episodeNFOIsComplete(path, aliasName string, episodeNumber int64, recordedAt time.Time, title, platform string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(content)
	title = sanitizeComponent(title)
	if title == "" {
		title = "未命名直播"
	}
	platform = sanitizeComponent(platform)
	if platform == "" {
		platform = "bililive-go"
	}
	displayTitle := fmt.Sprintf("%s - %s", recordedAt.Format("2006-01-02"), title)
	return strings.Contains(text, fmt.Sprintf("<title>%s</title>", xmlEscape(displayTitle))) &&
		strings.Contains(text, fmt.Sprintf("<showtitle>%s</showtitle>", xmlEscape(aliasName))) &&
		strings.Contains(text, "<season>1</season>") &&
		strings.Contains(text, fmt.Sprintf("<episode>%d</episode>", episodeNumber)) &&
		strings.Contains(text, fmt.Sprintf("<studio>%s</studio>", xmlEscape(platform)))
}

func showNFOIsComplete(path, aliasName string, recordedAt time.Time, platform string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(content)
	return showNFOIdentityMatches(text, aliasName) &&
		strings.Contains(text, "<year>") &&
		strings.Contains(text, "<studio>") &&
		strings.Contains(text, `<thumb aspect="poster">poster.jpg</thumb>`)
}

func showNFOIdentityMatches(text, aliasName string) bool {
	var nfo struct {
		XMLName   xml.Name `xml:"tvshow"`
		Title     string   `xml:"title"`
		ShowTitle string   `xml:"showtitle"`
	}
	if err := xml.Unmarshal([]byte(text), &nfo); err != nil || nfo.XMLName.Local != "tvshow" {
		return false
	}
	return strings.TrimSpace(nfo.Title) == aliasName &&
		strings.TrimSpace(nfo.ShowTitle) == aliasName
}

func ensureShowNFOFields(text string, recordedAt time.Time, platform string) string {
	platform = sanitizeComponent(platform)
	if platform == "" {
		platform = "bililive-go"
	}
	const closingTag = "</tvshow>"
	index := strings.LastIndex(text, closingTag)
	if index < 0 {
		return text
	}
	var additions []string
	if !strings.Contains(text, "<year>") {
		additions = append(additions, fmt.Sprintf("  <year>%d</year>", recordedAt.Year()))
	}
	if !strings.Contains(text, "<studio>") {
		additions = append(additions, fmt.Sprintf("  <studio>%s</studio>", xmlEscape(platform)))
	}
	if !strings.Contains(text, `<thumb aspect="poster">poster.jpg</thumb>`) {
		additions = append(additions, `  <thumb aspect="poster">poster.jpg</thumb>`)
	}
	if len(additions) == 0 {
		return text
	}
	return text[:index] + strings.Join(additions, "\n") + "\n" + text[index:]
}

func ensureLibraryShowNFO(showDir string, meta sourceFileMeta, platform string) error {
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		return err
	}
	nfoPath := filepath.Join(showDir, "tvshow.nfo")
	if showNFOIsComplete(nfoPath, meta.aliasName, meta.recordedAt, platform) {
		return nil
	}
	nfo := ""
	if current, err := os.ReadFile(nfoPath); err == nil && showNFOIdentityMatches(string(current), meta.aliasName) {
		nfo = ensureShowNFOFields(string(current), meta.recordedAt, platform)
	} else {
		nfo = buildShowNFO(meta.aliasName, meta.recordedAt, platform)
	}
	if err := ensureTextFile(nfoPath, nfo); err != nil {
		return fmt.Errorf("EnsureLibraryHardlink: write show NFO %s: %w", nfoPath, err)
	}
	return nil
}

func ensureLibraryEpisodeSidecars(ctx context.Context, sourcePath, targetPath string, meta sourceFileMeta, platform string) error {
	episodeNumber := episodeNumberFromLibraryPath(targetPath)
	if episodeNumber <= 0 {
		return nil
	}

	stem := sidecarStem(targetPath)
	seasonDir := filepath.Dir(targetPath)
	showDir := filepath.Dir(seasonDir)
	if err := ensureLibraryShowNFO(showDir, meta, platform); err != nil {
		return err
	}
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		return err
	}

	nfoPath := stem + ".nfo"
	if !episodeNFOIsComplete(nfoPath, meta.aliasName, episodeNumber, meta.recordedAt, meta.title, platform) {
		nfo := buildEpisodeNFO(meta.aliasName, episodeNumber, meta.recordedAt, meta.title, platform)
		if err := ensureTextFile(nfoPath, nfo); err != nil {
			return fmt.Errorf("EnsureLibraryHardlink: write NFO %s: %w", nfoPath, err)
		}
	}

	coverPath := stem + ".jpg"
	if info, err := os.Stat(coverPath); err == nil && info.Size() > 0 {
		return EnsureLibraryShowPoster(targetPath)
	}
	if _, err := extractCoverTo(ctx, sourcePath, coverPath); err != nil {
		logrus.WithFields(logrus.Fields{
			"source": sourcePath,
			"cover":  coverPath,
			"error":  err,
		}).Warn("EnsureLibraryHardlink: failed to extract episode cover")
		return fmt.Errorf("EnsureLibraryHardlink: extract episode cover %s: %w", coverPath, err)
	}
	if info, err := os.Stat(coverPath); err != nil || info.Size() == 0 {
		if err != nil {
			return fmt.Errorf("EnsureLibraryHardlink: stat extracted episode cover %s: %w", coverPath, err)
		}
		return fmt.Errorf("EnsureLibraryHardlink: extracted episode cover is empty: %s", coverPath)
	}
	return EnsureLibraryShowPoster(targetPath)
}

// EnsureLibraryShowPoster 从真实单集画面创建稳定的合集封面。
// 已有非空封面会保留，重复或增量发布不会覆盖用户封面。
func EnsureLibraryShowPoster(libraryPath string) error {
	coverPath := sidecarStem(libraryPath) + ".jpg"
	showDir := filepath.Dir(filepath.Dir(libraryPath))
	return ensureLibraryShowPosterFromCover(showDir, coverPath)
}

func ensureLibraryShowPosterFromCover(showDir, coverPath string) error {
	coverInfo, err := os.Stat(coverPath)
	if err != nil {
		return fmt.Errorf("EnsureLibraryHardlink: stat episode cover %s: %w", coverPath, err)
	}
	if coverInfo.Size() == 0 {
		return fmt.Errorf("EnsureLibraryHardlink: episode cover is empty: %s", coverPath)
	}

	posterPath := filepath.Join(showDir, "poster.jpg")
	unlock := lockLibraryPath(&libraryShowPosterLocks, posterPath)
	defer unlock()

	if posterInfo, statErr := os.Stat(posterPath); statErr == nil {
		if posterInfo.Size() > 0 {
			return nil
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("EnsureLibraryHardlink: stat show poster %s: %w", posterPath, statErr)
	}

	source, err := os.Open(coverPath)
	if err != nil {
		return fmt.Errorf("EnsureLibraryHardlink: open episode cover %s: %w", coverPath, err)
	}
	defer source.Close()

	temp, err := os.CreateTemp(showDir, ".poster-*.jpg")
	if err != nil {
		return fmt.Errorf("EnsureLibraryHardlink: create temporary show poster in %s: %w", showDir, err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := io.Copy(temp, source); err != nil {
		_ = temp.Close()
		return fmt.Errorf("EnsureLibraryHardlink: copy show poster %s: %w", posterPath, err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("EnsureLibraryHardlink: sync show poster %s: %w", posterPath, err)
	}
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return fmt.Errorf("EnsureLibraryHardlink: chmod show poster %s: %w", posterPath, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("EnsureLibraryHardlink: close show poster %s: %w", posterPath, err)
	}

	if err := os.Link(tempPath, posterPath); err == nil {
		return nil
	} else if !os.IsExist(err) {
		return fmt.Errorf("EnsureLibraryHardlink: create show poster %s from %s: %w", posterPath, coverPath, err)
	}

	posterInfo, err := os.Stat(posterPath)
	if err == nil && posterInfo.Size() > 0 {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("EnsureLibraryHardlink: stat show poster %s: %w", posterPath, err)
	}
	if err == nil {
		if err := os.Remove(posterPath); err != nil {
			return fmt.Errorf("EnsureLibraryHardlink: remove empty show poster %s: %w", posterPath, err)
		}
	}
	if err := os.Link(tempPath, posterPath); err != nil {
		if os.IsExist(err) {
			if posterInfo, statErr := os.Stat(posterPath); statErr == nil && posterInfo.Size() > 0 {
				return nil
			}
		}
		return fmt.Errorf("EnsureLibraryHardlink: replace empty show poster %s from %s: %w", posterPath, coverPath, err)
	}
	return nil
}

func prepareLibraryEpisodePublication(
	ctx context.Context,
	sourcePath string,
	stagedVideoPath string,
	targetPath string,
	meta sourceFileMeta,
	platform string,
) (string, string, error) {
	episodeNumber := episodeNumberFromLibraryPath(targetPath)
	if episodeNumber <= 0 {
		return "", "", fmt.Errorf("cannot parse episode number from target: %s", targetPath)
	}

	stagedStem := sidecarStem(stagedVideoPath)
	nfoPath := stagedStem + ".nfo"
	if err := writeFileAtomically(
		nfoPath,
		[]byte(buildEpisodeNFO(meta.aliasName, episodeNumber, meta.recordedAt, meta.title, platform)),
		0o644,
	); err != nil {
		return "", "", err
	}

	coverPath := stagedStem + ".jpg"
	if _, err := extractCoverTo(ctx, sourcePath, coverPath); err != nil {
		return "", "", fmt.Errorf("EnsureLibraryHardlink: extract staged episode cover %s: %w", coverPath, err)
	}
	if info, err := os.Stat(coverPath); err != nil || info.Size() == 0 {
		if err != nil {
			return "", "", fmt.Errorf("EnsureLibraryHardlink: stat staged episode cover %s: %w", coverPath, err)
		}
		return "", "", fmt.Errorf("EnsureLibraryHardlink: staged episode cover is empty: %s", coverPath)
	}

	showDir := filepath.Dir(filepath.Dir(targetPath))
	if err := ensureLibraryShowNFO(showDir, meta, platform); err != nil {
		return "", "", err
	}
	if err := ensureLibraryShowPosterFromCover(showDir, coverPath); err != nil {
		return "", "", err
	}
	return nfoPath, coverPath, nil
}

func EnsureLibrarySidecars(ctx context.Context, sourcePath, libraryPath, fallbackHost string, fallbackTime time.Time, platform string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	unlock := lockLibraryPath(&libraryEpisodePublishLocks, filepath.Dir(libraryPath))
	defer unlock()
	meta, ok := parseLibraryEpisodeFilename(libraryPath)
	if !ok {
		meta = parseSourceFilename(sourcePath, fallbackHost, fallbackTime)
	}
	if meta.aliasName == "" {
		meta.aliasName = sanitizeComponent(fallbackHost)
	}
	if meta.aliasName == "" {
		meta.aliasName = "未分类主播"
	}
	if meta.title == "" {
		meta.title = "未命名直播"
	}

	coverSource := sourcePath
	if _, err := os.Stat(coverSource); err != nil {
		coverSource = libraryPath
	}
	return ensureLibraryEpisodeSidecars(ctx, coverSource, libraryPath, meta, platform)
}

func newLibraryPublishStagingPath(libraryRoot, targetPath string) (string, func(), error) {
	absoluteLibraryRoot, err := filepath.Abs(libraryRoot)
	if err != nil {
		return "", nil, err
	}
	absoluteLibraryRoot, err = filepath.EvalSymlinks(absoluteLibraryRoot)
	if err != nil {
		return "", nil, err
	}
	parent := filepath.Dir(absoluteLibraryRoot)
	if parent == absoluteLibraryRoot {
		return "", nil, fmt.Errorf("cannot stage library publication outside root: %s", libraryRoot)
	}
	stagingParent := filepath.Join(parent, ".library_publish_staging")
	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return "", nil, err
	}
	stagingParent, err = filepath.EvalSymlinks(stagingParent)
	if err != nil {
		return "", nil, err
	}
	relative, err := filepath.Rel(absoluteLibraryRoot, stagingParent)
	if err != nil {
		return "", nil, err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)) {
		return "", nil, fmt.Errorf("library publication staging resolves inside library root: %s", stagingParent)
	}
	transactionRoot, err := os.MkdirTemp(stagingParent, "episode-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(transactionRoot)
	}
	return filepath.Join(transactionRoot, filepath.Base(targetPath)), cleanup, nil
}

func captureLibraryPublishedHardlink(staged, target string) (libraryPublishedHardlink, error) {
	info, err := os.Stat(staged)
	if err != nil {
		return libraryPublishedHardlink{}, err
	}
	file := libraryPublishedHardlink{
		staged:  staged,
		target:  target,
		size:    info.Size(),
		modTime: info.ModTime(),
	}
	if extension := strings.ToLower(filepath.Ext(staged)); extension == ".nfo" || extension == ".jpg" {
		content, err := os.ReadFile(staged)
		if err != nil {
			return libraryPublishedHardlink{}, err
		}
		file.digest = sha256.Sum256(content)
		file.checkDigest = true
	}
	return file, nil
}

func publishedHardlinkUnchanged(file libraryPublishedHardlink) error {
	stagedInfo, stagedErr := os.Stat(file.staged)
	targetInfo, targetErr := os.Stat(file.target)
	if os.IsNotExist(targetErr) {
		return nil
	}
	if stagedErr != nil || targetErr != nil {
		return fmt.Errorf("inspect published file %s: %v / %v", file.target, stagedErr, targetErr)
	}
	if !os.SameFile(stagedInfo, targetInfo) ||
		stagedInfo.Size() != file.size ||
		!stagedInfo.ModTime().Equal(file.modTime) {
		return fmt.Errorf("published target changed before rollback: %s", file.target)
	}
	if file.checkDigest {
		content, err := os.ReadFile(file.staged)
		if err != nil {
			return err
		}
		if sha256.Sum256(content) != file.digest {
			return fmt.Errorf("published target content changed before rollback: %s", file.target)
		}
	}
	return nil
}

func validateLibraryPublishedHardlinks(published []libraryPublishedHardlink) error {
	for _, file := range published {
		if err := publishedHardlinkUnchanged(file); err != nil {
			return err
		}
	}
	return nil
}

func removeLibraryPublishedHardlinks(published []libraryPublishedHardlink) error {
	var rollbackErr error
	for index := len(published) - 1; index >= 0; index-- {
		if err := os.Remove(published[index].target); err != nil && !os.IsNotExist(err) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove published file %s: %w", published[index].target, err))
		}
	}
	return rollbackErr
}

func snapshotLibraryShowFiles(showDir string) ([]libraryFileSnapshot, error) {
	snapshots := make([]libraryFileSnapshot, 0, 2)
	for _, filename := range []string{"tvshow.nfo", "poster.jpg"} {
		path := filepath.Join(showDir, filename)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			snapshots = append(snapshots, libraryFileSnapshot{path: path})
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("show metadata is not a regular file: %s", path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, libraryFileSnapshot{
			path:    path,
			existed: true,
			content: content,
			mode:    info.Mode().Perm(),
			info:    info,
		})
	}
	return snapshots, nil
}

func persistLibraryShowSnapshots(transactionRoot string, snapshots []libraryFileSnapshot) error {
	backupDir := filepath.Join(transactionRoot, ".show-backup")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	manifest := make([]libraryShowSnapshotManifestEntry, 0, len(snapshots))
	for index, snapshot := range snapshots {
		entry := libraryShowSnapshotManifestEntry{
			Path:    snapshot.path,
			Existed: snapshot.existed,
			Mode:    snapshot.mode,
		}
		if snapshot.existed {
			entry.Backup = fmt.Sprintf("%03d-%s", index, filepath.Base(snapshot.path))
			backupPath := filepath.Join(backupDir, entry.Backup)
			file, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, snapshot.mode)
			if err != nil {
				return err
			}
			if _, err := file.Write(snapshot.content); err != nil {
				_ = file.Close()
				return err
			}
			if err := file.Sync(); err != nil {
				_ = file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		}
		manifest = append(manifest, entry)
	}
	manifestContent, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestFile, err := os.OpenFile(filepath.Join(backupDir, "manifest.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := manifestFile.Write(append(manifestContent, '\n')); err != nil {
		_ = manifestFile.Close()
		return err
	}
	if err := manifestFile.Sync(); err != nil {
		_ = manifestFile.Close()
		return err
	}
	if err := manifestFile.Close(); err != nil {
		return err
	}
	directory, err := os.Open(backupDir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func sameLibraryFileSnapshot(left, right libraryFileSnapshot) bool {
	if left.path != right.path || left.existed != right.existed {
		return false
	}
	if !left.existed {
		return true
	}
	return os.SameFile(left.info, right.info) &&
		left.mode == right.mode &&
		bytes.Equal(left.content, right.content)
}

func restoreLibraryShowFiles(before, applied []libraryFileSnapshot) error {
	if err := validateLibraryShowFiles(before, applied); err != nil {
		return err
	}
	return restoreValidatedLibraryShowFiles(before, applied)
}

func validateLibraryShowFiles(before, applied []libraryFileSnapshot) error {
	if len(before) != len(applied) || len(before) == 0 {
		return errors.New("show metadata snapshot mismatch")
	}
	current, err := snapshotLibraryShowFiles(filepath.Dir(before[0].path))
	if err != nil {
		return err
	}
	for index := range before {
		if sameLibraryFileSnapshot(before[index], applied[index]) {
			continue
		}
		if !sameLibraryFileSnapshot(current[index], applied[index]) {
			return fmt.Errorf("show metadata changed after publication snapshot: %s", before[index].path)
		}
	}
	return nil
}

func restoreValidatedLibraryShowFiles(before, applied []libraryFileSnapshot) error {
	var restoreErr error
	for index, snapshot := range before {
		if sameLibraryFileSnapshot(snapshot, applied[index]) {
			continue
		}
		if snapshot.existed {
			if err := writeFileAtomically(snapshot.path, snapshot.content, snapshot.mode); err != nil {
				restoreErr = errors.Join(restoreErr, fmt.Errorf("restore %s: %w", snapshot.path, err))
			}
			continue
		}
		if err := os.Remove(snapshot.path); err != nil && !os.IsNotExist(err) {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("remove new show metadata %s: %w", snapshot.path, err))
		}
	}
	return restoreErr
}

// EnsureLibraryHardlink creates a Plex-style hard-link for sourcePath inside
// libraryRoot when the normal host-side organizer cron hasn't run yet.
//
// 目标路径：<libraryRoot>/<aliasName>/Season 01/<aliasName>.S01E<identity>.<date> - <title>.mp4。
//
// Idempotent: if any file inside Season 01 already shares the same inode as
// sourcePath, that path is returned as-is — no new file is created.
//
// 集号由 2020 年以来的微秒数和小型碰撞槽组成，保留精确顺序并限制在
// JavaScript 安全整数范围内。
//
// 同进程内按季度目录串行发布；最终无覆盖硬链接只会占用选定槽位，
// 或在外部冲突时拒绝发布该单集的暂存 sidecar。
func EnsureLibraryHardlink(ctx context.Context, sourcePath, libraryRoot, fallbackHost string, fallbackTime time.Time, platform string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	ext := filepath.Ext(sourcePath)
	meta := parseSourceFilename(sourcePath, fallbackHost, fallbackTime)

	seasonDir := filepath.Join(libraryRoot, meta.aliasName, "Season 01")
	unlock := lockLibraryPath(&libraryEpisodePublishLocks, seasonDir)
	defer unlock()

	// Step 0: if a completed subtitle sidecar already records this source path,
	// prefer its rendered output. Burning usually replaces the hardlink inode, so
	// inode-only idempotency would otherwise publish the leftover source again.
	if existingOutput := findExistingMetadataOutputInDir(sourcePath, seasonDir); existingOutput != "" {
		if err := ensureLibraryEpisodeSidecars(ctx, sourcePath, existingOutput, meta, platform); err != nil {
			return "", err
		}
		return existingOutput, nil
	}

	// Step 1: check if this source is ALREADY hardlinked somewhere in the season dir.
	existingLink, err := findExistingHardlinkInDir(sourcePath, seasonDir)
	if err == nil && existingLink != "" {
		if err := ensureLibraryEpisodeSidecars(ctx, sourcePath, existingLink, meta, platform); err != nil {
			return "", err
		}
		// Idempotent — already linked.
		return existingLink, nil
	}

	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		return "", fmt.Errorf("EnsureLibraryHardlink: mkdirAll %s: %w", seasonDir, err)
	}

	// 第二步：从真实录制开始时间推导稳定且有序的身份。
	episodeNumber := episodeNumberForRecordedAt(meta.recordedAt)
	if episodeNumber < 0 {
		return "", fmt.Errorf("EnsureLibraryHardlink: recording time exceeds safe episode identity range: %s", meta.recordedAt.Format(time.RFC3339Nano))
	}
	if episodeNumber == 0 {
		return "", fmt.Errorf("EnsureLibraryHardlink: no reliable recording start time: %s", sourcePath)
	}
	identityEnd := episodeNumber + chronologicalEpisodeIdentityBase
	for episodeNumber < identityEnd {
		reserved, reserveErr := episodeNumberReserved(seasonDir, episodeNumber)
		if reserveErr != nil {
			return "", fmt.Errorf("EnsureLibraryHardlink: scan recording-time identity: %w", reserveErr)
		}
		if !reserved {
			break
		}
		episodeNumber++
	}
	if episodeNumber == identityEnd {
		return "", fmt.Errorf("EnsureLibraryHardlink: recording-time identity collision space exhausted: %s", seasonDir)
	}

	targetName := buildEpisodeFilename(meta.aliasName, episodeNumber, meta.recordedAt, meta.title, ext)
	targetPath := filepath.Join(seasonDir, targetName)

	if _, statErr := os.Stat(targetPath); statErr == nil {
		return "", fmt.Errorf("EnsureLibraryHardlink: recording-time identity already occupied: %s", targetPath)
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("EnsureLibraryHardlink: stat %s: %w", targetPath, statErr)
	}

	stagedPath, cleanupStaging, err := newLibraryPublishStagingPath(libraryRoot, targetPath)
	if err != nil {
		return "", fmt.Errorf("EnsureLibraryHardlink: prepare staging for %s: %w", targetPath, err)
	}
	if err := os.Link(sourcePath, stagedPath); err != nil {
		cleanupStaging()
		return "", fmt.Errorf("EnsureLibraryHardlink: stage hardlink %s → %s: %w", sourcePath, stagedPath, err)
	}
	showDir := filepath.Dir(seasonDir)
	showBefore, err := snapshotLibraryShowFiles(showDir)
	if err != nil {
		cleanupStaging()
		return "", fmt.Errorf("EnsureLibraryHardlink: snapshot show metadata: %w", err)
	}
	if err := persistLibraryShowSnapshots(filepath.Dir(stagedPath), showBefore); err != nil {
		cleanupStaging()
		return "", fmt.Errorf("EnsureLibraryHardlink: persist show metadata snapshot: %w", err)
	}
	stagedNFOPath, stagedCoverPath, prepareErr := prepareLibraryEpisodePublication(
		ctx,
		sourcePath,
		stagedPath,
		targetPath,
		meta,
		platform,
	)
	showApplied, snapshotErr := snapshotLibraryShowFiles(showDir)
	if prepareErr != nil || snapshotErr != nil {
		var recoveryErr error
		if snapshotErr == nil {
			recoveryErr = restoreLibraryShowFiles(showBefore, showApplied)
		}
		if recoveryErr != nil || snapshotErr != nil {
			return "", fmt.Errorf(
				"%w; show metadata rollback failed: %v; preserved staging: %s",
				errors.Join(prepareErr, snapshotErr),
				recoveryErr,
				filepath.Dir(stagedPath),
			)
		}
		cleanupStaging()
		return "", prepareErr
	}

	var published []libraryPublishedHardlink
	rollbackPublication := func() error {
		if recoveryErr := validateLibraryPublishedHardlinks(published); recoveryErr != nil {
			return recoveryErr
		}
		showValidationErr := validateLibraryShowFiles(showBefore, showApplied)
		if recoveryErr := removeLibraryPublishedHardlinks(published); recoveryErr != nil {
			return recoveryErr
		}
		recoveryErr := showValidationErr
		if recoveryErr == nil {
			recoveryErr = restoreValidatedLibraryShowFiles(showBefore, showApplied)
		}
		if recoveryErr == nil {
			cleanupStaging()
		}
		return recoveryErr
	}
	recoverPublication := func(primary error) error {
		if recoveryErr := rollbackPublication(); recoveryErr != nil {
			return fmt.Errorf("%w; publication rollback failed: %v; preserved staging: %s", primary, recoveryErr, filepath.Dir(stagedPath))
		}
		return primary
	}

	targetStem := sidecarStem(targetPath)
	targetNFOPath := targetStem + ".nfo"
	publishedNFO, err := captureLibraryPublishedHardlink(stagedNFOPath, targetNFOPath)
	if err != nil {
		return "", recoverPublication(fmt.Errorf("EnsureLibraryHardlink: capture staged episode NFO: %w", err))
	}
	if err := librarySidecarLink(stagedNFOPath, targetNFOPath); err != nil {
		return "", recoverPublication(fmt.Errorf("EnsureLibraryHardlink: publish episode NFO %s: %w", targetNFOPath, err))
	}
	published = append(published, publishedNFO)
	targetCoverPath := targetStem + ".jpg"
	publishedCover, err := captureLibraryPublishedHardlink(stagedCoverPath, targetCoverPath)
	if err != nil {
		return "", recoverPublication(fmt.Errorf("EnsureLibraryHardlink: capture staged episode cover: %w", err))
	}
	if err := librarySidecarLink(stagedCoverPath, targetCoverPath); err != nil {
		return "", recoverPublication(fmt.Errorf("EnsureLibraryHardlink: publish episode cover %s: %w", targetCoverPath, err))
	}
	published = append(published, publishedCover)

	// 第三步：全部身份 sidecar 就绪后，最后再让 MP4 可见。
	err = libraryHardlinkLink(stagedPath, targetPath)
	if os.IsExist(err) {
		existingLink, scanErr := findExistingHardlinkInDir(sourcePath, seasonDir)
		if scanErr == nil && existingLink != "" {
			sameTarget, pathErr := sameCleanPath(existingLink, targetPath)
			if pathErr == nil && sameTarget {
				cleanupStaging()
				return existingLink, nil
			}
			if recoveryErr := rollbackPublication(); recoveryErr != nil {
				return "", fmt.Errorf(
					"EnsureLibraryHardlink: target occupied during publication: %s; publication rollback failed: %v; preserved staging: %s",
					targetPath,
					recoveryErr,
					filepath.Dir(stagedPath),
				)
			}
			if sidecarErr := ensureLibraryEpisodeSidecars(ctx, sourcePath, existingLink, meta, platform); sidecarErr != nil {
				return "", sidecarErr
			}
			return existingLink, nil
		}
		return "", recoverPublication(fmt.Errorf("EnsureLibraryHardlink: target occupied during publication: %s", targetPath))
	}
	if err != nil {
		return "", recoverPublication(fmt.Errorf("EnsureLibraryHardlink: publish hardlink %s → %s: %w", stagedPath, targetPath, err))
	}
	cleanupStaging()
	logrus.WithFields(logrus.Fields{
		"source":  sourcePath,
		"target":  targetPath,
		"episode": episodeNumber,
	}).Info("EnsureLibraryHardlink: 已为源文件创建字幕库硬链接（未等待 cron）")
	return targetPath, nil
}
