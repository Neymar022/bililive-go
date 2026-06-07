package subtitle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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

var libraryEpisodeSlotPattern = regexp.MustCompile(`\.S01E(\d{4})\.`)

// invalidFilenameChars mirrors INVALID_FILENAME_CHARS in bililive_media_organizer.py.
var invalidFilenameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`)

var extractCoverTo = tools.ExtractCoverTo

// sanitizeComponent replaces invalid filename characters with spaces, collapses
// multiple whitespace, and trims leading/trailing spaces and dots.
// Mirrors sanitize_component() in bililive_media_organizer.py.
func sanitizeComponent(s string) string {
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
// Format: <aliasName>.S01E####.<YYYY-MM-DD> - <title><extension>
// Mirrors build_episode_filename() in bililive_tv_library_builder.py.
func buildEpisodeFilename(aliasName string, episodeNumber int, recordedAt time.Time, title, extension string) string {
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

func buildEpisodeNFO(aliasName string, episodeNumber int, recordedAt time.Time, title, platform string) string {
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

// nextAvailableEpisodeNumber returns the first S01E slot not already used by
// any file in the season directory. Sidecars reserve slots too, because a stale
// .srt/.ass/.subtitle.json without an mp4 must not be matched to a new source.
func nextAvailableEpisodeNumber(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}
	used := make(map[int]struct{}, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		match := libraryEpisodeSlotPattern.FindStringSubmatch(e.Name())
		if match == nil {
			continue
		}
		episodeNumber, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		used[episodeNumber] = struct{}{}
	}
	for episodeNumber := 1; ; episodeNumber++ {
		if _, ok := used[episodeNumber]; !ok {
			return episodeNumber
		}
	}
}

func episodeNumberFromLibraryPath(path string) int {
	match := libraryEpisodeSlotPattern.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return 0
	}
	episodeNumber, err := strconv.Atoi(match[1])
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

	recordedAt, err := time.ParseInLocation("2006-01-02 15-04-05", recordedAtStr, time.Local)
	if err != nil {
		recordedAt = fallbackTime
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

func ensureTextFile(path, text string) error {
	if current, err := os.ReadFile(path); err == nil && string(current) == text {
		return nil
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func episodeNFOIsComplete(path, aliasName string, episodeNumber int, recordedAt time.Time, title, platform string) bool {
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
	platform = sanitizeComponent(platform)
	if platform == "" {
		platform = "bililive-go"
	}
	return strings.Contains(text, "<tvshow>") &&
		strings.Contains(text, fmt.Sprintf("<title>%s</title>", xmlEscape(aliasName))) &&
		strings.Contains(text, fmt.Sprintf("<showtitle>%s</showtitle>", xmlEscape(aliasName))) &&
		strings.Contains(text, fmt.Sprintf("<year>%d</year>", recordedAt.Year())) &&
		strings.Contains(text, fmt.Sprintf("<studio>%s</studio>", xmlEscape(platform)))
}

func ensureLibraryShowNFO(showDir string, meta sourceFileMeta, platform string) error {
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		return err
	}
	nfoPath := filepath.Join(showDir, "tvshow.nfo")
	if showNFOIsComplete(nfoPath, meta.aliasName, meta.recordedAt, platform) {
		return nil
	}
	nfo := buildShowNFO(meta.aliasName, meta.recordedAt, platform)
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
		return nil
	}
	if _, err := extractCoverTo(ctx, sourcePath, coverPath); err != nil {
		logrus.WithFields(logrus.Fields{
			"source": sourcePath,
			"cover":  coverPath,
			"error":  err,
		}).Warn("EnsureLibraryHardlink: failed to extract episode cover")
	}
	return nil
}

// EnsureLibraryHardlink creates a Plex-style hard-link for sourcePath inside
// libraryRoot when the normal host-side organizer cron hasn't run yet.
//
// Path constructed: <libraryRoot>/<aliasName>/Season 01/<aliasName>.S01E####.<date> - <title>.mp4
//
// Idempotent: if any file inside Season 01 already shares the same inode as
// sourcePath, that path is returned as-is — no new file is created.
//
// Episode numbering: first free S01E slot in Season 01, considering sidecars
// as slot reservations. This avoids attaching stale subtitles to a newer
// recording after the rendered video was removed.
//
// Race-safe: a concurrent call will either find the same-inode entry in the
// scan above, or will race on os.Link which returns EEXIST and then retry the
// next free slot after checking whether our source already appeared.
func EnsureLibraryHardlink(ctx context.Context, sourcePath, libraryRoot, fallbackHost string, fallbackTime time.Time, platform string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	ext := filepath.Ext(sourcePath)
	meta := parseSourceFilename(sourcePath, fallbackHost, fallbackTime)

	seasonDir := filepath.Join(libraryRoot, meta.aliasName, "Season 01")

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

	// Step 2: compute the first episode slot not reserved by videos or sidecars.
	episodeNumber := nextAvailableEpisodeNumber(seasonDir)

	for {
		targetName := buildEpisodeFilename(meta.aliasName, episodeNumber, meta.recordedAt, meta.title, ext)
		targetPath := filepath.Join(seasonDir, targetName)

		if _, statErr := os.Stat(targetPath); statErr == nil {
			// The exact slot is already occupied by a different inode. Try the
			// next episode number without touching its sidecars.
			episodeNumber++
			continue
		} else if !os.IsNotExist(statErr) {
			return "", fmt.Errorf("EnsureLibraryHardlink: stat %s: %w", targetPath, statErr)
		}

		if err := ensureLibraryEpisodeSidecars(ctx, sourcePath, targetPath, meta, platform); err != nil {
			return "", err
		}

		// Step 3: create hard-link without overwriting any occupied slot.
		err := os.Link(sourcePath, targetPath)
		if err == nil {
			logrus.WithFields(logrus.Fields{
				"source":  sourcePath,
				"target":  targetPath,
				"episode": episodeNumber,
			}).Info("EnsureLibraryHardlink: 已为源文件创建字幕库硬链接（未等待 cron）")

			return targetPath, nil
		}
		if os.IsExist(err) {
			// Concurrent call already created this exact slot — verify it's our link.
			existingLink, scanErr := findExistingHardlinkInDir(sourcePath, seasonDir)
			if scanErr == nil && existingLink != "" {
				return existingLink, nil
			}
			episodeNumber++
			continue
		}
		return "", fmt.Errorf("EnsureLibraryHardlink: os.Link %s → %s: %w", sourcePath, targetPath, err)
	}
}
