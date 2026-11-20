package pipeline

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/subtitle"
)

// LibraryIndexSnapshotObserver 只消费 NAS 本机固定 SELECT 产出的快照。
// 它不会连接 PG、执行 shell、修补索引或将未知观察当成成功。
type LibraryIndexSnapshotObserver struct {
	path string
	now  func() time.Time
}

func NewLibraryIndexSnapshotObserver(path string) LibraryIndexObserver {
	return &LibraryIndexSnapshotObserver{path: path, now: time.Now}
}

type librarySnapshot struct {
	SchemaVersion  int                              `json:"schema_version"`
	AdapterVersion string                           `json:"adapter_version"`
	GeneratedAt    time.Time                        `json:"generated_at"`
	LibraryRoot    string                           `json:"library_root"`
	Platform       subtitle.LibraryRecoveryPlatform `json:"platform"`
	Libraries      []struct {
		ID        int64    `json:"id"`
		Roots     []string `json:"roots"`
		ReadLocal *bool    `json:"read_local_info_first"`
		WriteNFO  *bool    `json:"nfo_info_save_to_local"`
	} `json:"libraries"`
	Files []librarySnapshotFile `json:"files"`
}

type librarySnapshotFile struct {
	Path         string `json:"path"`
	FileID       int64  `json:"file_id"`
	Size         int64  `json:"file_size"`
	Category     string `json:"category_id"`
	Library      int64  `json:"library_id"`
	Season       int    `json:"season"`
	Episode      int64  `json:"episode"`
	OrdinalCount int64  `json:"ordinal_file_count"`
	Audio        int64  `json:"audio_count"`
	Video        int64  `json:"video_count"`
	Episodes     []struct {
		ID       int64    `json:"id"`
		Category string   `json:"category_id"`
		Library  int64    `json:"library_id"`
		Season   int      `json:"season"`
		Episode  int64    `json:"episode"`
		Paths    []string `json:"paths"`
	} `json:"episodes"`
	Shows []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Category string `json:"category_id"`
		Library  int64  `json:"library_id"`
	} `json:"shows"`
}

func (o *LibraryIndexSnapshotObserver) VerifyPublication(ctx context.Context, expected LibraryIndexExpected) (LibraryIndexObservation, error) {
	result := LibraryIndexObservation{State: "index_unconfirmed"}
	unknown := func(reason string) (LibraryIndexObservation, error) { result.Reason = reason; return result, nil }
	conflict := func(reason string) (LibraryIndexObservation, error) {
		result.State = "external_index_conflict"
		result.Reason = reason
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if o.path == "" {
		return unknown("snapshot_not_configured")
	}
	info, err := os.Lstat(o.path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return unknown("snapshot_unavailable")
	}
	f, err := os.Open(o.path)
	if err != nil {
		return unknown("snapshot_unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return unknown("snapshot_changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return unknown("snapshot_unreadable")
	}
	var snapshot librarySnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.SchemaVersion != 2 || snapshot.AdapterVersion != "ugreen-fixed-select-v2" {
		return unknown("snapshot_schema_unknown")
	}
	result.GeneratedAt, result.AdapterVersion = snapshot.GeneratedAt, snapshot.AdapterVersion
	age := o.now().Sub(snapshot.GeneratedAt)
	if snapshot.GeneratedAt.IsZero() || age > 90*time.Second || age < -5*time.Second {
		return unknown("snapshot_stale_or_future")
	}
	if err := subtitle.ValidateLibraryRecoveryPlatform(snapshot.Platform, o.now()); err != nil {
		return unknown("platform_unconfirmed")
	}
	result.Platform = snapshot.Platform
	if !expected.PublishedAt.IsZero() && !snapshot.GeneratedAt.After(expected.PublishedAt) {
		return unknown("snapshot_precedes_publication")
	}
	root := filepath.Clean(expected.LibraryRoot)
	season := filepath.Clean(expected.Season)
	if !filepath.IsAbs(root) || root != snapshot.LibraryRoot || !strings.HasPrefix(season, root+string(os.PathSeparator)) || filepath.Dir(filepath.Dir(season)) != root {
		return unknown("library_root_or_season_mismatch")
	}
	if expected.Show == "" || filepath.Base(filepath.Dir(season)) != expected.Show {
		return unknown("show_path_mismatch")
	}
	if len(snapshot.Libraries) != 1 {
		return unknown("library_config_unknown")
	}
	library := snapshot.Libraries[0]
	if library.ID != 4 || len(library.Roots) != 1 || library.Roots[0] != root || library.ReadLocal == nil || library.WriteNFO == nil {
		return unknown("library_config_changed")
	}
	if !*library.ReadLocal || *library.WriteNFO {
		return unknown("library_nfo_writeback_enabled_or_local_read_disabled")
	}
	entries, err := os.ReadDir(season)
	if err != nil {
		return unknown("season_unreadable")
	}
	media := make(map[string]os.FileInfo)
	for _, entry := range entries {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".mp4" && ext != ".mkv" {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return unknown("season_media_invalid")
		}
		media[filepath.Join(season, entry.Name())] = info
	}
	byPath := make(map[string][]librarySnapshotFile)
	for _, item := range snapshot.Files {
		if filepath.Dir(item.Path) == season {
			byPath[item.Path] = append(byPath[item.Path], item)
		}
	}
	if len(media) == 0 {
		return unknown("season_media_empty")
	}
	exemptions, scopeErr := libraryIndexScope(&expected)
	if scopeErr != nil {
		return unknown("append_scope_invalid: " + scopeErr.Error())
	}
	if !expected.PublishedAt.IsZero() && !snapshot.GeneratedAt.After(expected.PublishedAt) {
		return unknown("snapshot_precedes_publication")
	}
	var protectedShow, protectedCategory string
	if expected.Scope != "" {
		for _, mapping := range expected.Protected {
			if protectedShow == "" {
				protectedShow = mapping.ShowID
				protectedCategory = mapping.CategoryID
			} else if mapping.ShowID != protectedShow || mapping.CategoryID != protectedCategory {
				return conflict("protected_season_identity_inconsistent")
			}
		}
	}
	if expected.Scope != "" {
		for path := range media {
			if err := subtitle.ValidatePublishedLibraryEpisode(path); err != nil {
				return conflict("append_local_ledger_conflict")
			}
			break
		}
	}
	targetMissing := false
	fileIDs := map[int64]string{}
	episodeIDs := map[int64]string{}
	mappings := make(map[string]LibraryIndexMapping)
	for path, info := range media {
		rows := byPath[path]
		if len(rows) == 0 {
			for _, before := range expected.Protected {
				if before.Path == path {
					return conflict("protected_mapping_missing")
				}
			}
			if exemption, ok := exemptions[path]; ok {
				if err := validateIndexExemption(path, exemption); err != nil {
					return conflict("unconfirmed_local_identity_changed")
				}
				if exemption.OperationID == "" {
					result.LegacyUnconfirmed = append(result.LegacyUnconfirmed, exemption)
				} else {
					result.OwnedIndexPending = append(result.OwnedIndexPending, exemption)
				}
				if path == expected.Path {
					targetMissing = true
				}
				continue
			}
			return unknown("media_not_indexed")
		}
		if len(rows) != 1 {
			return conflict("duplicate_file_mapping")
		}
		row := rows[0]
		if row.Library != 4 || row.FileID <= 0 || row.Category == "" || row.Season != 1 || row.Episode < 1 || row.Episode > 9999 || row.OrdinalCount != 1 {
			return conflict("file_mapping_invalid")
		}
		if len(row.Episodes) != 1 || len(row.Shows) != 1 {
			return conflict("episode_or_show_mapping_not_unique")
		}
		episode, show := row.Episodes[0], row.Shows[0]
		if expected.Scope != "" && protectedShow != "" && (strconv.FormatInt(show.ID, 10) != protectedShow || row.Category != protectedCategory) {
			return conflict("season_show_or_category_changed")
		}
		if expected.Scope != "" {
			if previous := fileIDs[row.FileID]; previous != "" && previous != path {
				return conflict("duplicate_file_identity")
			}
			if previous := episodeIDs[episode.ID]; previous != "" && previous != path {
				return conflict("duplicate_episode_identity")
			}
			fileIDs[row.FileID] = path
			episodeIDs[episode.ID] = path
		}
		if episode.ID <= 0 || episode.Category != row.Category || episode.Library != row.Library || episode.Season != row.Season || episode.Episode != row.Episode || show.ID <= 0 || show.Category != row.Category || show.Library != row.Library || strings.TrimSpace(show.Name) != expected.Show {
			return conflict("episode_or_show_mapping_mismatch")
		}
		if len(episode.Paths) > 0 {
			matched := false
			for _, p := range episode.Paths {
				if p == path {
					matched = true
				}
			}
			if !matched {
				return conflict("episode_path_mismatch")
			}
		}
		if row.Size != info.Size() || row.Audio < 1 || row.Video < 1 {
			return unknown("media_index_not_ready")
		}
		nfo, err := os.ReadFile(strings.TrimSuffix(path, filepath.Ext(path)) + ".nfo")
		if err != nil || len(nfo) > 4<<20 {
			return unknown("nfo_unreadable")
		}
		var ordinal struct {
			XMLName xml.Name `xml:"episodedetails"`
			Season  []int    `xml:"season"`
			Episode []int64  `xml:"episode"`
		}
		if xml.Unmarshal(nfo, &ordinal) != nil || len(ordinal.Season) != 1 || len(ordinal.Episode) != 1 || ordinal.Season[0] != row.Season || ordinal.Episode[0] != row.Episode {
			return conflict("nfo_index_ordinal_mismatch")
		}
		mapping := LibraryIndexMapping{Path: path, FileID: strconv.FormatInt(row.FileID, 10), EpisodeID: strconv.FormatInt(episode.ID, 10), ShowID: strconv.FormatInt(show.ID, 10), CategoryID: row.Category, LibraryID: strconv.FormatInt(row.Library, 10), Ordinal: row.Episode, SeasonNumber: row.Season}
		if exemption, ok := exemptions[path]; ok && exemption.OperationID != "" && !snapshot.GeneratedAt.After(exemption.PublishedAt) {
			// 其他operation的旧快照也不能升级成保护mapping；仍保留其owned pending。
			result.OwnedIndexPending = append(result.OwnedIndexPending, exemption)
			if path == expected.Path {
				targetMissing = true
			}
			continue
		}
		mappings[path] = mapping
	}
	for path := range byPath {
		if _, exists := media[path]; !exists {
			return conflict("indexed_media_missing")
		}
	}
	for _, before := range expected.Protected {
		after, ok := mappings[before.Path]
		if !ok || before != after {
			return conflict("protected_mapping_changed")
		}
	}
	if expected.Path != "" {
		mapping, exists := mappings[expected.Path]
		if !exists {
			if targetMissing {
				result.Reason = "publication_not_indexed"
			} else {
				return unknown("publication_not_indexed")
			}
		}
		if expected.Ordinal <= 0 || expected.MediaSize <= 0 {
			return unknown("publication_expectation_incomplete")
		}
		if exists && (mapping.Ordinal != expected.Ordinal || media[expected.Path].Size() != expected.MediaSize) {
			return conflict("publication_expectation_mismatch")
		}
		// 身份与媒体revision由正式发布者在调用前后验证；此处检查独立NFO身份。
		raw, err := os.ReadFile(strings.TrimSuffix(expected.Path, filepath.Ext(expected.Path)) + ".nfo")
		var nfo struct {
			IDs []struct {
				Type  string `xml:"type,attr"`
				Value string `xml:",chardata"`
			} `xml:"uniqueid"`
		}
		if err != nil || xml.Unmarshal(raw, &nfo) != nil || expected.Identity == "" {
			return unknown("publication_identity_unreadable")
		}
		count := 0
		for _, id := range nfo.IDs {
			if id.Type == "bililive-recorded-at" {
				count++
				if strings.TrimSpace(id.Value) != expected.Identity {
					return conflict("publication_identity_mismatch")
				}
			}
		}
		if count != 1 {
			return conflict("publication_identity_count")
		}
	}
	for _, mapping := range mappings {
		result.Mappings = append(result.Mappings, mapping)
	}
	sort.Slice(result.Mappings, func(i, j int) bool { return result.Mappings[i].Path < result.Mappings[j].Path })
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if targetMissing {
		result.State = "index_unconfirmed"
		return result, nil
	}
	result.State, result.Reason = "verified", fmt.Sprintf("%d exact media mappings", len(result.Mappings))
	if expected.Scope == "append_by_admission" && expected.Path != "" {
		result.State = "target_verified"
	}
	if expected.Scope == "append_bootstrap" {
		result.State = "bootstrap_verified"
	}
	return result, nil
}

func libraryIndexScope(expected *LibraryIndexExpected) (map[string]LibraryIndexUnconfirmed, error) {
	result := map[string]LibraryIndexUnconfirmed{}
	if expected.Scope == "" {
		if len(expected.BaselineLegacyUnconfirmed) > 0 || len(expected.OwnedIndexPending) > 0 || expected.LegacyAdmission != nil {
			return nil, fmt.Errorf("exemptions require explicit scope")
		}
		return result, nil
	}
	if expected.Scope != "append_by_admission" && expected.Scope != "append_bootstrap" {
		return nil, fmt.Errorf("unknown scope")
	}
	if expected.Scope == "append_bootstrap" {
		if len(expected.OwnedIndexPending) > 0 {
			return nil, fmt.Errorf("bootstrap cannot claim pending ownership")
		}
		if _, err := subtitle.LoadLibraryAssignments(expected.Season); err == nil {
			return nil, fmt.Errorf("bootstrap already enabled")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		var baseline []subtitle.LibraryLegacyAssignment
		for _, m := range expected.Protected {
			id, e := strconv.ParseInt(libraryPathIdentity(m.Path), 10, 64)
			if e != nil {
				return nil, e
			}
			baseline = append(baseline, subtitle.LibraryLegacyAssignment{Path: m.Path, Identity: id, Ordinal: m.Ordinal})
		}
		for _, e := range expected.BaselineLegacyUnconfirmed {
			id, err := strconv.ParseInt(e.Identity, 10, 64)
			if err != nil {
				return nil, err
			}
			baseline = append(baseline, subtitle.LibraryLegacyAssignment{Path: e.Path, Identity: id, Ordinal: e.Ordinal})
		}
		approved, err := subtitle.ValidateLibraryLegacyAdmission(expected.Season, expected.LegacyAdmission, baseline)
		if err != nil {
			return nil, err
		}
		for _, e := range expected.BaselineLegacyUnconfirmed {
			_, explicit := approved[e.Path]
			if e.OperationID != "" || !explicit || filepath.Dir(e.Path) != expected.Season || result[e.Path].Path != "" {
				return nil, fmt.Errorf("unapproved bootstrap unknown")
			}
			if err := validateIndexExemption(e.Path, e); err != nil {
				return nil, err
			}
			result[e.Path] = e
		}
		return result, nil
	}
	ledger, err := subtitle.LoadLibraryAssignments(expected.Season)
	if err != nil {
		return nil, err
	}
	if expected.LegacyAdmission != nil && !equalJSON(expected.LegacyAdmission, ledger.LegacyAdmission) {
		return nil, fmt.Errorf("runtime approval differs from durable ledger")
	}
	approved, err := subtitle.ValidateLibraryLegacyAdmission(expected.Season, ledger.LegacyAdmission, ledger.LegacyEntries)
	if err != nil {
		return nil, err
	}
	protected := map[string]LibraryIndexMapping{}
	for _, m := range expected.Protected {
		protected[m.Path] = m
	}
	addKnown := func(m *subtitle.AssignmentIndexMapping) error {
		if m == nil {
			return nil
		}
		value := indexMappingFromAssignment(*m)
		if before, ok := protected[value.Path]; ok && before != value {
			return fmt.Errorf("protected expectation conflict")
		}
		protected[value.Path] = value
		return nil
	}
	for _, e := range ledger.LegacyEntries {
		if err = addKnown(e.KnownIndexMapping); err != nil {
			return nil, err
		}
		if e.IndexState == "legacy_unconfirmed" && e.KnownIndexMapping == nil {
			_, explicit := approved[e.Path]
			historical := ledger.LegacyAdmission == nil && (e.Ordinal == 36 || e.Ordinal == 82)
			if !explicit && !historical {
				return nil, fmt.Errorf("legacy unknown not bootstrap permitted")
			}
			result[e.Path] = LibraryIndexUnconfirmed{Path: e.Path, Identity: strconv.FormatInt(e.Identity, 10), Ordinal: e.Ordinal}
		}
	}
	for _, a := range ledger.Assignments {
		if a.State != "published" {
			continue
		}
		if a.TargetPath == expected.Path {
			if expected.AssignmentOperation != "" && expected.AssignmentOperation != a.OperationID {
				return nil, fmt.Errorf("target assignment operation changed")
			}
			if a.PublishedAt == nil {
				return nil, fmt.Errorf("published assignment has no durable time")
			}
			if expected.PublishedAt.Before(*a.PublishedAt) {
				expected.PublishedAt = *a.PublishedAt
			}
		}
		if err = addKnown(a.KnownIndexMapping); err != nil {
			return nil, err
		}
		if a.KnownIndexMapping == nil {
			if a.PublishedAt == nil {
				return nil, fmt.Errorf("published assignment has no durable time")
			}
			result[a.TargetPath] = LibraryIndexUnconfirmed{Path: a.TargetPath, Identity: strconv.FormatInt(a.Identity, 10), Ordinal: a.Ordinal, OperationID: a.OperationID, PublishedAt: *a.PublishedAt}
		}
	}
	// 外部调用不能把任意缺失media塞进豁免集合。
	for _, e := range append(append([]LibraryIndexUnconfirmed{}, expected.BaselineLegacyUnconfirmed...), expected.OwnedIndexPending...) {
		if before, ok := result[e.Path]; !ok || before != e {
			return nil, fmt.Errorf("unconfirmed scope differs from ledger")
		}
	}
	expected.Protected = nil
	for _, m := range protected {
		expected.Protected = append(expected.Protected, m)
	}
	return result, nil
}
func validateIndexExemption(path string, e LibraryIndexUnconfirmed) error {
	if e.Path != path || libraryPathIdentity(path) != e.Identity || e.Identity == "" {
		return fmt.Errorf("unconfirmed identity mismatch")
	}
	ordinal, err := subtitle.PublishedLibraryOrdinal(path)
	if err != nil || ordinal != e.Ordinal {
		return fmt.Errorf("unconfirmed ordinal mismatch")
	}
	return subtitle.ValidatePublishedLibraryEpisode(path)
}
func indexMappingFromAssignment(m subtitle.AssignmentIndexMapping) LibraryIndexMapping {
	return LibraryIndexMapping{Path: m.Path, FileID: m.FileID, EpisodeID: m.EpisodeID, ShowID: m.ShowID, SeasonID: m.SeasonID, CategoryID: m.CategoryID, LibraryID: m.LibraryID, Ordinal: m.Ordinal, SeasonNumber: m.SeasonNumber}
}
func assignmentMappingFromIndex(m LibraryIndexMapping) subtitle.AssignmentIndexMapping {
	return subtitle.AssignmentIndexMapping{Path: m.Path, FileID: m.FileID, EpisodeID: m.EpisodeID, ShowID: m.ShowID, SeasonID: m.SeasonID, CategoryID: m.CategoryID, LibraryID: m.LibraryID, Ordinal: m.Ordinal, SeasonNumber: m.SeasonNumber}
}
