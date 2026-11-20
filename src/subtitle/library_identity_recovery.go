package subtitle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const LibraryPlatformStartupPath = "/var/targets/video_serv"
const LibraryPlatformBuildPath = "/volume1/@appstore/com.ugreen.videomgr/sbin/video_serv"

// LibraryRecoveryPlatform 由宿主只读 observer 证明；PID 仅区分采集实例，不进入能力签名。
type LibraryRecoveryPlatform struct {
	State       string    `json:"state"`
	Reason      string    `json:"reason,omitempty"`
	PID         int       `json:"pid"`
	UID         int       `json:"uid"`
	StartTicks  uint64    `json:"start_ticks"`
	StartedAt   time.Time `json:"started_at"`
	ObservedAt  time.Time `json:"observed_at"`
	StartupPath string    `json:"startup_path"`
	BuildPath   string    `json:"build_path"`
	BuildSHA256 string    `json:"build_sha256"`
}

func validRecoverySHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func ValidateLibraryRecoveryPlatform(p LibraryRecoveryPlatform, now time.Time) error {
	age := now.Sub(p.ObservedAt)
	if p.State != "confirmed" || p.PID <= 0 || p.UID < 0 || p.StartTicks == 0 || p.StartedAt.IsZero() || p.ObservedAt.IsZero() || p.StartedAt.After(p.ObservedAt) || age > 90*time.Second || age < -5*time.Second || p.StartupPath != LibraryPlatformStartupPath || p.BuildPath != LibraryPlatformBuildPath || !validRecoverySHA256(p.BuildSHA256) {
		return recoveryRefuse("platform_unconfirmed", p.BuildPath)
	}
	return nil
}

// LibraryRecoveryError 是已分类的拒绝原因，不允许把拒绝算作修复成功。
type LibraryRecoveryError struct{ Reason, Path string }

func (e *LibraryRecoveryError) Error() string  { return e.Reason + ": " + e.Path }
func recoveryRefuse(reason, path string) error { return &LibraryRecoveryError{reason, path} }

// LibraryFileState 绑定文件身份、内容及完整 Linux 属性；媒体仅取身份/大小/mtime，绝不移动。
type LibraryFileState struct {
	Path          string
	Device, Inode uint64
	Size, MtimeNS int64
	UID, GID      uint32
	Mode          uint32
	Xattrs        map[string][]byte
	SHA           string
}
type LibraryIdentityMember struct {
	Media         LibraryFileState
	Sidecars      []LibraryFileState
	NFO           LibraryFileState
	Before, After []byte
	Identity      int64
	Ordinal       int64
	RecordedAt    time.Time
}
type LibraryIdentityPlan struct {
	Entries []string
	Version int
	Season  string
	Parents []LibraryFileState
	Members []LibraryIdentityMember
}

func LibraryRecoveryRoot(library string) string {
	return filepath.Join(filepath.Dir(library), "."+filepath.Base(library)+".library-recovery")
}
func LibraryRecoveryDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
func (p *LibraryIdentityPlan) Hash() string    { b, _ := json.Marshal(p); return LibraryRecoveryDigest(b) }

// PlanLibraryIdentityRecovery 只接受完整、连续且已按时间排序的 Season 01。
// 缺失身份必须由 metadata 精确 start_time 与长文件 identity 相互印证。
func PlanLibraryIdentityRecovery(season string) (*LibraryIdentityPlan, error) {
	absolute, err := filepath.Abs(season)
	if err != nil {
		return nil, err
	}
	if filepath.Base(absolute) != "Season 01" {
		return nil, recoveryRefuse("unsupported_season", absolute)
	}
	p := &LibraryIdentityPlan{Version: 1, Season: absolute}
	for parent := absolute; ; parent = filepath.Dir(parent) {
		state, err := CaptureLibraryFileState(parent, false)
		if err != nil {
			return nil, err
		}
		p.Parents = append(p.Parents, state)
		if parent == filepath.Dir(filepath.Dir(absolute)) {
			break
		}
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	stems := map[string]bool{}
	publishedIDs := map[int64]string{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		p.Entries = append(p.Entries, entry.Name())
		path := filepath.Join(absolute, entry.Name())
		if entry.IsDir() {
			return nil, recoveryRefuse("unknown_season_member", path)
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".mp4" && ext != ".mkv" {
			if _, ok := libraryEpisodeStem(entry.Name()); !ok {
				return nil, recoveryRefuse("unknown_season_member", path)
			}
			continue
		}
		slot := libraryEpisodeSlotPattern.FindStringSubmatch(entry.Name())
		if slot == nil || len(slot) > 2 && slot[2] != "" {
			return nil, recoveryRefuse("unsupported_media_identity", path)
		}
		id, err := strconv.ParseInt(slot[1], 10, 64)
		if err != nil {
			return nil, recoveryRefuse("identity_conflict", path)
		}
		stem := sidecarStem(path)
		if stems[stem] || ids[id] {
			return nil, recoveryRefuse("identity_conflict", path)
		}
		stems[stem], ids[id] = true, true
		media, err := CaptureLibraryFileState(path, false)
		if err != nil {
			return nil, err
		}
		if media.Size == 0 {
			return nil, recoveryRefuse("media_invalid", path)
		}
		nfo, err := CaptureLibraryFileState(stem+".nfo", true)
		if err != nil {
			return nil, recoveryRefuse("nfo_invalid", stem+".nfo")
		}
		if err := libraryRecoverySingleLink(nfo.Path); err != nil {
			return nil, err
		}
		before, err := os.ReadFile(nfo.Path)
		if err != nil {
			return nil, err
		}
		ordinal, count, value, offset, err := parseRecoveryNFO(before)
		if err != nil {
			return nil, recoveryRefuse("nfo_invalid", nfo.Path)
		}
		if ordinal < 1 || ordinal > maxUGREENEpisodeOrdinal {
			return nil, recoveryRefuse("ordinal_repair_required", nfo.Path)
		}
		member := LibraryIdentityMember{Media: media, NFO: nfo, Before: before, After: before, Identity: id, Ordinal: ordinal}
		for _, suffix := range []string{".subtitle.json", ".srt", ".ass", ".jpg", ".transcript.json"} {
			state, err := CaptureLibraryFileState(stem+suffix, true)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			member.Sidecars = append(member.Sidecars, state)
		}
		recorded, exact := recordedAtForEpisodeIdentity(id)
		if count == 0 {
			if !exact {
				return nil, recoveryRefuse("identity_evidence_missing", path)
			}
			meta, err := LoadMetadata(stem + ".subtitle.json")
			if err != nil || meta.Status != StatusCompleted || filepath.Clean(meta.OutputPath) != path {
				return nil, recoveryRefuse("identity_evidence_missing", path)
			}
			raw, _ := meta.RecordMeta["start_time"].(string)
			start, err := CheckLibraryIdentityEvidence(path, meta.OutputPath, raw)
			if err != nil {
				return nil, err
			}
			recorded = start
			insertion := []byte(fmt.Sprintf(`<uniqueid type="bililive-recorded-at" default="false">%d</uniqueid>`, id))
			member.After = append(append(append([]byte{}, before[:offset]...), insertion...), before[offset:]...)
			_, newCount, newID, _, err := parseRecoveryNFO(member.After)
			if err != nil || newCount != 1 || newID != id {
				return nil, recoveryRefuse("nfo_invalid", path)
			}
		} else {
			if count != 1 {
				return nil, recoveryRefuse("identity_conflict", path)
			}
			if !exact {
				recorded, exact = episodeRecordedAtFromSidecars(stem)
			}
			base := episodeIdentityForRecordedAt(recorded)
			if !exact || value < base || value >= base+chronologicalEpisodeIdentityBase || (id > maxUGREENEpisodeOrdinal && value != id) {
				return nil, recoveryRefuse("identity_conflict", path)
			}
			member.Identity = value
		}
		if previous, duplicate := publishedIDs[member.Identity]; duplicate {
			return nil, recoveryRefuse("identity_conflict", previous+" and "+path)
		}
		publishedIDs[member.Identity] = path
		member.RecordedAt = recorded
		p.Members = append(p.Members, member)
	}
	sort.Slice(p.Members, func(i, j int) bool {
		a, b := p.Members[i], p.Members[j]
		if a.RecordedAt.Equal(b.RecordedAt) {
			return a.Identity < b.Identity
		}
		return a.RecordedAt.Before(b.RecordedAt)
	})
	for i, m := range p.Members {
		if m.Ordinal != int64(i+1) {
			return nil, recoveryRefuse("ordinal_repair_required", m.NFO.Path)
		}
	}
	return p, nil
}

func parseRecoveryNFO(data []byte) (ordinal int64, count int, identity int64, offset int, err error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, episodes, roots, seasons := 0, 0, 0, 0
	for {
		before := int(d.InputOffset())
		token, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			err = e
			return
		}
		switch v := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				roots++
				if v.Name.Local != "episodedetails" || v.Name.Space != "" {
					err = fmt.Errorf("bad root")
					return
				}
			}
			if depth == 2 && v.Name.Local == "season" {
				seasons++
				var value int
				err = d.DecodeElement(&value, &v)
				if err != nil {
					return
				}
				depth--
				if value != 1 {
					err = fmt.Errorf("unsupported NFO season")
					return
				}
			}
			if depth == 2 && v.Name.Local == "episode" {
				episodes++
				var text string
				err = d.DecodeElement(&text, &v)
				if err != nil {
					return
				}
				ordinal, err = strconv.ParseInt(strings.TrimSpace(text), 10, 64)
				if err != nil {
					return
				}
				depth--
			}
			if depth == 2 && v.Name.Local == "uniqueid" {
				kind := ""
				for _, a := range v.Attr {
					if a.Name.Local == "type" {
						kind = a.Value
					}
				}
				var text string
				err = d.DecodeElement(&text, &v)
				if err != nil {
					return
				}
				depth--
				if kind == "bililive-recorded-at" {
					count++
					identity, err = strconv.ParseInt(strings.TrimSpace(text), 10, 64)
					if err != nil {
						return
					}
				}
			}
		case xml.EndElement:
			if depth == 1 {
				offset = before
			}
			depth--
		}
	}
	if roots != 1 || seasons != 1 || episodes != 1 || depth != 0 || offset == 0 {
		err = fmt.Errorf("ambiguous NFO")
	}
	return
}

// ValidateLibraryIdentityPlan 先检查全体再动任何文件；目标只允许已知 before/after。
func ValidateLibraryIdentityPlan(p *LibraryIdentityPlan, publishedPaths ...string) error {
	current, err := os.ReadDir(p.Season)
	if err != nil {
		return err
	}
	var entries []string
	media := map[string]bool{}
	for _, e := range current {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		allowed := false
		for _, published := range publishedPaths {
			stem := sidecarStem(published)
			candidate := filepath.Join(p.Season, e.Name())
			if candidate == published {
				allowed = true
			}
			for _, suffix := range []string{".nfo", ".srt", ".ass", ".jpg", ".subtitle.json", ".transcript.json"} {
				if candidate == stem+suffix {
					allowed = true
				}
			}
		}
		if allowed {
			continue
		}
		entries = append(entries, e.Name())
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() {
			return recoveryRefuse("season_members_changed", e.Name())
		}
		if ext == ".mp4" || ext == ".mkv" {
			media[filepath.Join(p.Season, e.Name())] = true
		}
	}
	if !reflect.DeepEqual(entries, p.Entries) {
		return recoveryRefuse("season_members_changed", p.Season)
	}
	if len(media) != len(p.Members) {
		return recoveryRefuse("season_members_changed", p.Season)
	}
	for _, parent := range p.Parents {
		now, err := CaptureLibraryFileState(parent.Path, false)
		if err != nil {
			return err
		}
		if !sameRecoveryAttributes(parent, now) || parent.Device != now.Device || parent.Inode != now.Inode {
			return recoveryRefuse("ancestor_attributes_changed", parent.Path)
		}
	}
	for _, m := range p.Members {
		if !media[m.Media.Path] {
			return recoveryRefuse("season_members_changed", m.Media.Path)
		}
		now, err := CaptureLibraryFileState(m.Media.Path, false)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(now, m.Media) {
			return recoveryRefuse("media_changed", m.Media.Path)
		}
		for _, s := range m.Sidecars {
			now, err := CaptureLibraryFileState(s.Path, true)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(s, now) {
				return recoveryRefuse("evidence_changed", s.Path)
			}
		}
		now, err = CaptureLibraryFileState(m.NFO.Path, true)
		if err != nil {
			return err
		}
		if now.SHA != LibraryRecoveryDigest(m.Before) && now.SHA != LibraryRecoveryDigest(m.After) {
			return recoveryRefuse("nfo_third_value", m.NFO.Path)
		}
		if !sameRecoveryAttributes(m.NFO, now) || now.MtimeNS != m.NFO.MtimeNS {
			return recoveryRefuse("nfo_attributes_changed", m.NFO.Path)
		}
		if err := libraryRecoverySingleLink(m.NFO.Path); err != nil {
			return err
		}
	}
	return nil
}
func sameRecoveryAttributes(a, b LibraryFileState) bool {
	return a.UID == b.UID && a.GID == b.GID && a.Mode == b.Mode && reflect.DeepEqual(a.Xattrs, b.Xattrs)
}

// LibrarySeasonPath 与现有发布命名使用相同净化规则。
func LibrarySeasonPath(root, host string) string {
	return canonicalLibrarySeason(filepath.Join(root, sanitizeComponent(host), "Season 01"))
}
func PublishedLibraryOrdinal(path string) (int64, error) {
	ordinal, _, err := publishedEpisodeMetadata(path, episodeIdentityFromLibraryPath(path))
	return ordinal, err
}

func LibraryRecoveryMarker(libraryPath string) string {
	season := canonicalLibrarySeason(filepath.Dir(libraryPath))
	return filepath.Join(libraryPublicationRecoveryRoot(season), "active-"+LibraryRecoveryDigest([]byte(season)))
}

func libraryPublicationRecoveryRoot(season string) string {
	if filepath.Base(season) == "Season 01" || (filepath.Base(season) == "global" && filepath.Base(filepath.Dir(season)) == ".library-recovery") {
		return LibraryRecoveryRoot(filepath.Dir(filepath.Dir(season)))
	}
	// 旧平铺 sidecar API 没有库/主播/Season 布局；不能向上猜根并要求根目录写权。
	return filepath.Join(season, ".bililive-library-locks")
}

// AssertLibraryPublicationReady 让普通 writer 同样遵守中断后留下的持久恢复阻塞。
func AssertLibraryPublicationReady(path string) error {
	marker := LibraryRecoveryMarker(path)
	if _, err := os.Lstat(marker); err == nil {
		return recoveryRefuse("season_recovery_in_progress", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}
func LockLibraryRecoveryWriter(ctx context.Context, library string) (func(), error) {
	return libraryPublicationOSLock(ctx, filepath.Join(library, ".library-recovery", "global"))
}

// CheckLibraryIdentityEvidence 只核可信来源与长身份；不证明完整季、权限或完成状态。
func CheckLibraryIdentityEvidence(mediaPath, outputPath, startTime string) (time.Time, error) {
	identity := episodeIdentityFromLibraryPath(mediaPath)
	if identity <= maxUGREENEpisodeOrdinal || filepath.Clean(outputPath) != filepath.Clean(mediaPath) {
		return time.Time{}, recoveryRefuse("identity_evidence_missing", mediaPath)
	}
	start, err := time.Parse(time.RFC3339Nano, startTime)
	if err != nil || start.IsZero() {
		return time.Time{}, recoveryRefuse("identity_evidence_missing", mediaPath)
	}
	base := episodeIdentityForRecordedAt(start)
	if identity < base || identity >= base+chronologicalEpisodeIdentityBase {
		return time.Time{}, recoveryRefuse("identity_evidence_conflict", mediaPath)
	}
	return start, nil
}

// LibrarySeasonForInput 复用正式文件命名解析，避免把host显示别名当公开目录身份。
func LibrarySeasonForInput(root, input, host string, recordedAt time.Time) string {
	if meta, ok := parseLibraryEpisodeFilename(input); ok {
		return LibrarySeasonPath(root, meta.aliasName)
	}
	meta := parseSourceFilename(input, host, recordedAt)
	return LibrarySeasonPath(root, meta.aliasName)
}
