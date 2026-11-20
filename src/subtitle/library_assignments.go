package subtitle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	uuid "github.com/satori/go.uuid"
)

const LibraryAssignmentsFilename = ".bililive-ordinal-assignments.json"

var ErrLibraryAssignmentWorkActive = errors.New("assignment_writer_active")
var ErrLibraryAssignmentPartialInstall = errors.New("assignment_partial_install_requires_owner_reentry")

type AssignmentIndexMapping struct {
	Path, FileID, EpisodeID, ShowID, SeasonID, CategoryID, LibraryID string
	Ordinal                                                          int64
	SeasonNumber                                                     int
}
type LibraryLegacyAssignment struct {
	Path              string                  `json:"path"`
	Identity          int64                   `json:"identity"`
	Ordinal           int64                   `json:"ordinal"`
	KnownIndexMapping *AssignmentIndexMapping `json:"known_index_mapping,omitempty"`
	IndexState        string                  `json:"index_state"`
}

type LibraryLegacyMedia struct {
	Exists  bool   `json:"exists"`
	Regular bool   `json:"regular"`
	Size    int64  `json:"size"`
	Device  uint64 `json:"dev"`
	Inode   uint64 `json:"inode"`
	MtimeNS int64  `json:"mtime_ns"`
}
type LibraryLegacyApprovalEntry struct {
	Path     string             `json:"path"`
	Identity string             `json:"identity"`
	Ordinal  int64              `json:"ordinal"`
	Media    LibraryLegacyMedia `json:"media"`
	Reason   string             `json:"reason"`
}
type LibraryLegacyApproval struct {
	Schema      int                          `json:"schema"`
	Season      string                       `json:"season"`
	ApprovedBy  string                       `json:"approved_by"`
	ApprovedAt  time.Time                    `json:"approved_at"`
	EvidenceRef string                       `json:"evidence_ref"`
	Entries     []LibraryLegacyApprovalEntry `json:"entries"`
}

// LibraryLegacyAdmission只在明确批准bootstrap时写入；runtime不读取ApprovalPath。
type LibraryLegacyAdmission struct {
	ApprovalPath string                `json:"approval_path"`
	Approval     LibraryLegacyApproval `json:"approval"`
}
type AssignmentFileIdentity struct {
	Path    string `json:"path"`
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtime_ns"`
}
type AssignmentInstall struct {
	Staged           string                 `json:"staged"`
	Target           string                 `json:"target"`
	Identity         AssignmentFileIdentity `json:"identity"`
	PreserveExisting bool                   `json:"preserve_existing,omitempty"`
}
type LibraryAssignment struct {
	OperationID       string                   `json:"operation_id"`
	SessionID         string                   `json:"session_id"`
	MemberTaskIDs     []int64                  `json:"member_task_ids"`
	TargetPath        string                   `json:"target_path"`
	Identity          int64                    `json:"identity"`
	Ordinal           int64                    `json:"ordinal"`
	State             string                   `json:"state"`
	AdmittedAt        time.Time                `json:"admitted_at"`
	PublishedAt       *time.Time               `json:"published_at,omitempty"`
	StagingRoot       string                   `json:"staging_root"`
	Files             []AssignmentInstall      `json:"files,omitempty"`
	KnownIndexMapping *AssignmentIndexMapping  `json:"known_index_mapping,omitempty"`
	IndexState        string                   `json:"index_state,omitempty"`
	Inputs            []AssignmentFileIdentity `json:"inputs,omitempty"`
	ContentVersion    string                   `json:"content_version,omitempty"`
	SourceSet         json.RawMessage          `json:"source_set,omitempty"`
}
type LibraryAssignmentLedger struct {
	Schema             int                       `json:"schema"`
	Policy             string                    `json:"policy"`
	SeasonPath         string                    `json:"season_path"`
	Revision           int64                     `json:"revision"`
	EnabledAt          time.Time                 `json:"enabled_at"`
	EnabledByOperation string                    `json:"enabled_by_operation"`
	LegacyEntries      []LibraryLegacyAssignment `json:"legacy_entries"`
	Assignments        []LibraryAssignment       `json:"assignments"`
	BlockedReason      string                    `json:"blocked_reason,omitempty"`
	LegacyAdmission    *LibraryLegacyAdmission   `json:"legacy_admission,omitempty"`
}
type LibraryAssignmentBootstrapPlan struct {
	SeasonPath      string                    `json:"season_path"`
	OperationID     string                    `json:"operation_id"`
	LegacyEntries   []LibraryLegacyAssignment `json:"legacy_entries"`
	Media           []AssignmentFileIdentity  `json:"media"`
	NFOBytes        map[string][]byte         `json:"nfo_bytes"`
	LegacyAdmission *LibraryLegacyAdmission   `json:"legacy_admission,omitempty"`
}

// ValidateLibraryLegacyAdmission绑定批准、原legacy身份及当前stat/NFO，不按ordinal普遍豁免。
func ValidateLibraryLegacyAdmission(season string, admission *LibraryLegacyAdmission, legacy []LibraryLegacyAssignment) (map[string]LibraryLegacyApprovalEntry, error) {
	result := map[string]LibraryLegacyApprovalEntry{}
	if admission == nil {
		return result, nil
	}
	validText := func(value string) bool {
		if value == "" || strings.TrimSpace(value) != value {
			return false
		}
		for _, r := range value {
			if r < 32 || r == 127 {
				return false
			}
		}
		return true
	}
	a := admission.Approval
	if !filepath.IsAbs(admission.ApprovalPath) || filepath.Clean(admission.ApprovalPath) != admission.ApprovalPath || a.Schema != 1 || a.Season != season || !validText(a.ApprovedBy) || !validText(a.EvidenceRef) || a.ApprovedAt.IsZero() || a.ApprovedAt.After(time.Now().Add(5*time.Second)) || len(a.Entries) == 0 {
		return nil, errors.New("legacy_approval_invalid: schema/season/audit fields")
	}
	before := map[string]LibraryLegacyAssignment{}
	for _, e := range legacy {
		if _, exists := before[e.Path]; exists {
			return nil, errors.New("legacy_approval_invalid: duplicate baseline path")
		}
		before[e.Path] = e
	}
	identities := map[string]bool{}
	ordinals := map[int64]bool{}
	for _, entry := range a.Entries {
		n, err := strconv.ParseUint(entry.Identity, 10, 53)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != entry.Identity || !filepath.IsAbs(entry.Path) || filepath.Clean(entry.Path) != entry.Path || filepath.Dir(entry.Path) != season || entry.Ordinal < 1 || entry.Ordinal > maxUGREENEpisodeOrdinal || entry.Reason != "existing_media_index_absent" || !entry.Media.Exists || !entry.Media.Regular || entry.Media.Size <= 0 || entry.Media.Inode == 0 || entry.Media.MtimeNS <= 0 {
			return nil, errors.New("legacy_approval_invalid: entry identity/path/stat/reason")
		}
		if _, exists := result[entry.Path]; exists || identities[entry.Identity] || ordinals[entry.Ordinal] {
			return nil, errors.New("legacy_approval_invalid: duplicate entry")
		}
		original, exists := before[entry.Path]
		if !exists || strconv.FormatInt(original.Identity, 10) != entry.Identity || original.Ordinal != entry.Ordinal {
			return nil, errors.New("legacy_approval_invalid: entry not in exact baseline")
		}
		current, e := AssignmentStat(entry.Path)
		if e != nil {
			return nil, e
		}
		if current.Size != entry.Media.Size || current.Device != entry.Media.Device || current.Inode != entry.Media.Inode || current.MtimeNS != entry.Media.MtimeNS {
			return nil, errors.New("legacy_approval_invalid: media changed")
		}
		ordinal, _, e := publishedEpisodeMetadata(entry.Path, int64(n))
		if e != nil || ordinal != entry.Ordinal {
			return nil, errors.New("legacy_approval_invalid: NFO identity/ordinal changed")
		}
		result[entry.Path] = entry
		identities[entry.Identity] = true
		ordinals[entry.Ordinal] = true
	}
	return result, nil
}

func validateLegacyUnknowns(legacy []LibraryLegacyAssignment, approved map[string]LibraryLegacyApprovalEntry, allowHistorical bool) error {
	for _, e := range legacy {
		if e.KnownIndexMapping != nil {
			continue
		}
		_, explicit := approved[e.Path]
		historical := allowHistorical && (e.Ordinal == 36 || e.Ordinal == 82)
		if e.IndexState != "legacy_unconfirmed" || (!explicit && !historical) {
			return errors.New("unapproved legacy mapping missing")
		}
	}
	return nil
}

// AssignmentSeason 持既有OS/season锁，短提交回调不得再次获取season锁。
type AssignmentSeason struct {
	ledger  *LibraryAssignmentLedger
	release func()
	closed  bool
}

func assignmentSeasonPath(season string) (string, error) {
	if !filepath.IsAbs(season) || filepath.Clean(season) != season {
		return "", errors.New("assignment season must be absolute and clean")
	}
	resolved, err := filepath.EvalSymlinks(season)
	if err != nil {
		return "", err
	}
	if resolved != season {
		return "", errors.New("assignment season symlink refused")
	}
	return filepath.Join(season, LibraryAssignmentsFilename), nil
}
func AssignmentStat(path string) (AssignmentFileIdentity, error) {
	var v AssignmentFileIdentity
	if err := assignmentPlatformCheck(); err != nil {
		return v, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return v, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return v, errors.New("assignment file must be nonempty regular")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return v, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil || real != abs {
		return v, errors.New("assignment file symlink refused")
	}
	return assignmentPlatformIdentity(abs, info)
}
func sameAssignmentFile(a, b AssignmentFileIdentity) bool { a.Path = ""; b.Path = ""; return a == b }
func LoadLibraryAssignments(season string) (*LibraryAssignmentLedger, error) {
	path, err := assignmentSeasonPath(season)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := assignmentPlatformCheck(); err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return nil, errors.New("invalid assignment ledger file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l LibraryAssignmentLedger
	if err = json.Unmarshal(raw, &l); err != nil {
		return nil, err
	}
	if l.Schema != 1 || l.Policy != "append_by_admission" || l.SeasonPath != season || l.Revision < 1 || l.EnabledByOperation == "" {
		return nil, errors.New("assignment ledger schema/owner mismatch")
	}
	return &l, nil
}
func LibraryAppendEnabled(season string) (bool, error) {
	_, err := LoadLibraryAssignments(season)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
func assignmentWrite(l *LibraryAssignmentLedger, noReplace bool) error {
	if err := assignmentPlatformCheck(); err != nil {
		return err
	}
	path, err := assignmentSeasonPath(l.SeasonPath)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(l.SeasonPath, ".assignments-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if noReplace {
		err = os.Link(f.Name(), path)
	} else {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(l.SeasonPath)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func assignmentMedia(season string) ([]LibraryLegacyAssignment, []AssignmentFileIdentity, map[string][]byte, error) {
	entries, err := os.ReadDir(season)
	if err != nil {
		return nil, nil, nil, err
	}
	var legacy []LibraryLegacyAssignment
	var media []AssignmentFileIdentity
	nfos := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".mp4" && ext != ".mkv" {
			continue
		}
		path := filepath.Join(season, e.Name())
		id := episodeIdentityFromLibraryPath(path)
		ordinal, _, err := publishedEpisodeMetadata(path, id)
		if err != nil {
			return nil, nil, nil, err
		}
		st, err := AssignmentStat(path)
		if err != nil {
			return nil, nil, nil, err
		}
		raw, err := os.ReadFile(sidecarStem(path) + ".nfo")
		if err != nil {
			return nil, nil, nil, err
		}
		legacy = append(legacy, LibraryLegacyAssignment{Path: path, Identity: id, Ordinal: ordinal})
		media = append(media, st)
		nfos[sidecarStem(path)+".nfo"] = raw
	}
	sort.Slice(legacy, func(i, j int) bool { return legacy[i].Ordinal < legacy[j].Ordinal })
	seen := map[int64]bool{}
	for i, e := range legacy {
		if seen[e.Identity] || e.Ordinal != int64(i+1) {
			return nil, nil, nil, errors.New("assignment baseline identity/ordinal conflict")
		}
		seen[e.Identity] = true
	}
	return legacy, media, nfos, nil
}
func PlanLibraryAssignments(ctx context.Context, season, operation string, mappings []AssignmentIndexMapping, legacyUnconfirmed []string, admissions ...*LibraryLegacyAdmission) (*LibraryAssignmentBootstrapPlan, error) {
	if err := assignmentPlatformCheck(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if operation == "" {
		return nil, errors.New("explicit bootstrap operation required")
	}
	if _, err := assignmentSeasonPath(season); err != nil {
		return nil, err
	}
	unlock, err := LockLibraryPublicationContext(ctx, filepath.Join(season, ".bootstrap"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err = LoadLibraryAssignments(season); err == nil {
		return nil, errors.New("season already enabled")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if _, err = os.Lstat(LibraryRecoveryMarker(filepath.Join(season, ".bootstrap"))); !os.IsNotExist(err) {
		return nil, errors.New("active season recovery blocks bootstrap")
	}
	legacy, media, nfos, err := assignmentMedia(season)
	if err != nil {
		return nil, err
	}
	var admission *LibraryLegacyAdmission
	if len(admissions) > 1 {
		return nil, errors.New("one explicit legacy approval required")
	}
	if len(admissions) == 1 {
		admission = admissions[0]
	}
	approved, err := ValidateLibraryLegacyAdmission(season, admission, legacy)
	if err != nil {
		return nil, err
	}
	for _, entry := range legacy {
		if err := ValidatePublishedLibraryEpisode(entry.Path); err != nil {
			return nil, err
		}
	}
	unknown := map[string]bool{}
	for _, path := range legacyUnconfirmed {
		if unknown[path] {
			return nil, errors.New("duplicate legacy unknown")
		}
		unknown[path] = true
	}
	known := map[string]AssignmentIndexMapping{}
	for _, m := range mappings {
		if _, ok := known[m.Path]; ok {
			return nil, errors.New("duplicate bootstrap mapping")
		}
		known[m.Path] = m
	}
	for i, e := range legacy {
		if m, ok := known[e.Path]; ok {
			if m.Ordinal != e.Ordinal || m.FileID == "" || m.EpisodeID == "" || m.ShowID == "" || m.CategoryID == "" || m.LibraryID == "" {
				return nil, errors.New("bootstrap mapping incomplete or conflicting")
			}
			copy := m
			legacy[i].KnownIndexMapping = &copy
			legacy[i].IndexState = "protected"
			delete(known, e.Path)
		} else if _, explicit := approved[e.Path]; unknown[e.Path] && explicit {
			legacy[i].IndexState = "legacy_unconfirmed"
			delete(unknown, e.Path)
		} else {
			return nil, errors.New("unapproved legacy mapping missing")
		}
	}
	if len(known) > 0 || len(unknown) > 0 {
		return nil, errors.New("bootstrap proof references unknown media")
	}
	return &LibraryAssignmentBootstrapPlan{SeasonPath: season, OperationID: operation, LegacyEntries: legacy, Media: media, NFOBytes: nfos, LegacyAdmission: admission}, nil
}
func ApplyLibraryAssignments(ctx context.Context, p *LibraryAssignmentBootstrapPlan) error {
	if err := assignmentPlatformCheck(); err != nil {
		return err
	}
	if p == nil || p.OperationID == "" {
		return errors.New("bootstrap plan required")
	}
	unlock, err := LockLibraryPublicationContext(ctx, filepath.Join(p.SeasonPath, ".bootstrap"))
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = LoadLibraryAssignments(p.SeasonPath); err == nil {
		return errors.New("bootstrap ledger already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	legacy, media, nfos, err := assignmentMedia(p.SeasonPath)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(media, p.Media) || !reflect.DeepEqual(nfos, p.NFOBytes) || len(legacy) != len(p.LegacyEntries) {
		return errors.New("bootstrap original bytes/paths changed")
	}
	for i, e := range legacy {
		b := p.LegacyEntries[i]
		if e.Path != b.Path || e.Identity != b.Identity || e.Ordinal != b.Ordinal {
			return errors.New("bootstrap baseline changed")
		}
	}
	approved, err := ValidateLibraryLegacyAdmission(p.SeasonPath, p.LegacyAdmission, p.LegacyEntries)
	if err != nil {
		return err
	}
	if err = validateLegacyUnknowns(p.LegacyEntries, approved, false); err != nil {
		return err
	}
	if _, err = os.Lstat(LibraryRecoveryMarker(filepath.Join(p.SeasonPath, ".bootstrap"))); !os.IsNotExist(err) {
		return errors.New("active recovery blocks bootstrap")
	}
	l := &LibraryAssignmentLedger{Schema: 1, Policy: "append_by_admission", SeasonPath: p.SeasonPath, Revision: 1, EnabledAt: time.Now().UTC(), EnabledByOperation: p.OperationID, LegacyEntries: p.LegacyEntries, Assignments: []LibraryAssignment{}, LegacyAdmission: p.LegacyAdmission}
	return assignmentWrite(l, true)
}
func LockLibraryAssignments(ctx context.Context, season string) (*AssignmentSeason, error) {
	if err := assignmentPlatformCheck(); err != nil {
		return nil, err
	}
	unlock, err := LockLibraryPublicationContext(ctx, filepath.Join(season, ".assignment"))
	if err != nil {
		return nil, err
	}
	l, err := LoadLibraryAssignments(season)
	if err != nil {
		unlock()
		return nil, err
	}
	s := &AssignmentSeason{ledger: l, release: unlock}
	if err = s.Validate(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func (s *AssignmentSeason) Close() {
	if !s.closed {
		s.closed = true
		s.release()
	}
}
func (s *AssignmentSeason) Ledger() LibraryAssignmentLedger { return *s.ledger }
func (s *AssignmentSeason) Save() error {
	if s.closed {
		return errors.New("season lease closed")
	}
	s.ledger.Revision++
	return assignmentWrite(s.ledger, false)
}
func assignmentStagingRoot(season, op string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(season))), ".live_session_segments", ".sidecar_staging", "append-"+op)
}
func (s *AssignmentSeason) Validate() error {
	if s.ledger.BlockedReason != "" {
		return errors.New("season_blocked: " + s.ledger.BlockedReason)
	}
	approved, err := ValidateLibraryLegacyAdmission(s.ledger.SeasonPath, s.ledger.LegacyAdmission, s.ledger.LegacyEntries)
	if err != nil {
		return err
	}
	if err = validateLegacyUnknowns(s.ledger.LegacyEntries, approved, s.ledger.LegacyAdmission == nil); err != nil {
		return err
	}
	actual, _, _, err := assignmentMedia(s.ledger.SeasonPath)
	if err != nil {
		return err
	}
	allowed := map[string]LibraryLegacyAssignment{}
	for _, e := range s.ledger.LegacyEntries {
		allowed[e.Path] = e
	}
	pending := 0
	operations := map[string]bool{}
	sessions := map[string]bool{}
	reservedOrdinals := map[int64]bool{}
	identities := map[int64]bool{}
	var publishedMax int64
	for _, e := range s.ledger.LegacyEntries {
		if reservedOrdinals[e.Ordinal] || identities[e.Identity] {
			return errors.New("legacy ledger conflict")
		}
		reservedOrdinals[e.Ordinal] = true
		identities[e.Identity] = true
		if e.Ordinal > publishedMax {
			publishedMax = e.Ordinal
		}
	}
	for _, a := range s.ledger.Assignments {
		if a.State == "aborted" {
			continue
		}
		if a.OperationID == "" || filepath.Base(a.OperationID) != a.OperationID || a.SessionID == "" || a.StagingRoot != assignmentStagingRoot(s.ledger.SeasonPath, a.OperationID) || operations[a.OperationID] || sessions[a.SessionID] || reservedOrdinals[a.Ordinal] || identities[a.Identity] || (a.State != "reserved" && a.State != "committing" && a.State != "published") {
			return errors.New("invalid assignment ownership")
		}
		operations[a.OperationID] = true
		sessions[a.SessionID] = true
		reservedOrdinals[a.Ordinal] = true
		identities[a.Identity] = true
		if a.State == "published" && a.Ordinal > publishedMax {
			publishedMax = a.Ordinal
		}
		if a.State != "published" {
			pending++
		}
		if _, ok := allowed[a.TargetPath]; ok {
			return errors.New("assignment duplicate target")
		}
		allowed[a.TargetPath] = LibraryLegacyAssignment{Path: a.TargetPath, Identity: a.Identity, Ordinal: a.Ordinal}
	}
	if pending > 1 {
		return errors.New("multiple unresolved reservations")
	}
	for _, a := range s.ledger.Assignments {
		if (a.State == "reserved" || a.State == "committing") && a.Ordinal != publishedMax+1 {
			return errors.New("reservation ordinal must follow committed maximum")
		}
	}
	ids := map[int64]bool{}
	ord := map[int64]bool{}
	for _, e := range actual {
		expected, ok := allowed[e.Path]
		if !ok || expected.Identity != e.Identity || expected.Ordinal != e.Ordinal || ids[e.Identity] || ord[e.Ordinal] {
			return errors.New("unowned or changed published media")
		}
		ids[e.Identity] = true
		ord[e.Ordinal] = true
		delete(allowed, e.Path)
	}
	for _, e := range s.ledger.LegacyEntries {
		if _, missing := allowed[e.Path]; missing {
			return errors.New("legacy media missing")
		}
	}
	for _, a := range s.ledger.Assignments {
		if a.State == "published" {
			if _, missing := allowed[a.TargetPath]; missing {
				return errors.New("published assignment media missing")
			}
		}
	}
	return nil
}
func (s *AssignmentSeason) Reserve(session, target string, members []int64) (LibraryAssignment, error) {
	if s.closed || session == "" || len(members) == 0 || filepath.Dir(target) != s.ledger.SeasonPath || filepath.Ext(target) != ".mp4" {
		return LibraryAssignment{}, errors.New("incomplete assignment candidate")
	}
	members = append([]int64(nil), members...)
	sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
	for i, id := range members {
		if id <= 0 || (i > 0 && id == members[i-1]) {
			return LibraryAssignment{}, errors.New("invalid members")
		}
	}
	id := episodeIdentityFromLibraryPath(target)
	if id <= maxUGREENEpisodeOrdinal {
		return LibraryAssignment{}, errors.New("recording identity required")
	}
	var max int64
	for _, e := range s.ledger.LegacyEntries {
		if e.Path == target || e.Identity == id {
			return LibraryAssignment{}, errors.New("legacy result/identity already exists")
		}
		if e.Ordinal > max {
			max = e.Ordinal
		}
	}
	for _, a := range s.ledger.Assignments {
		if a.State == "aborted" {
			continue
		}
		if a.SessionID == session {
			if a.TargetPath != target || a.Identity != id || !reflect.DeepEqual(a.MemberTaskIDs, members) {
				return a, errors.New("same session assignment changed")
			}
			return a, nil
		}
		if a.TargetPath == target || a.Identity == id {
			return a, errors.New("assignment target identity conflict")
		}
		if a.State != "published" {
			return a, errors.New("season_reservation_pending")
		}
		if a.Ordinal > max {
			max = a.Ordinal
		}
	}
	if max >= maxUGREENEpisodeOrdinal {
		return LibraryAssignment{}, errors.New("ordinal limit reached")
	}
	if reserved, err := episodeIdentityReserved(s.ledger.SeasonPath, id); err != nil {
		return LibraryAssignment{}, err
	} else if reserved {
		return LibraryAssignment{}, errors.New("exact recording identity already reserved by sidecar")
	}
	for _, ext := range []string{".mp4", ".srt", ".ass", ".nfo", ".jpg", ".subtitle.json"} {
		if _, err := os.Lstat(strings.TrimSuffix(target, ".mp4") + ext); !os.IsNotExist(err) {
			return LibraryAssignment{}, errors.New("exact assignment target already exists")
		}
	}
	value, err := uuid.NewV4()
	if err != nil {
		return LibraryAssignment{}, err
	}
	op := value.String()
	a := LibraryAssignment{OperationID: op, SessionID: session, MemberTaskIDs: members, TargetPath: target, Identity: id, Ordinal: max + 1, State: "reserved", AdmittedAt: time.Now().UTC(), StagingRoot: assignmentStagingRoot(s.ledger.SeasonPath, op), IndexState: "owned_index_pending"}
	s.ledger.Assignments = append(s.ledger.Assignments, a)
	return a, s.Save()
}
func ReserveLibraryAssignment(ctx context.Context, season, session, target string, members []int64) (LibraryAssignment, int64, error) {
	s, err := LockLibraryAssignments(ctx, season)
	if err != nil {
		return LibraryAssignment{}, 0, err
	}
	defer s.Close()
	a, err := s.Reserve(session, target, members)
	return a, s.ledger.Revision, err
}
func (s *AssignmentSeason) Find(op string) (*LibraryAssignment, error) {
	for i := range s.ledger.Assignments {
		if s.ledger.Assignments[i].OperationID == op {
			return &s.ledger.Assignments[i], nil
		}
	}
	return nil, errors.New("assignment operation not found")
}
func AssignmentOrdinal(season, target string) (int64, bool, error) {
	l, err := LoadLibraryAssignments(season)
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, true, err
	}
	s := &AssignmentSeason{ledger: l}
	if err = s.Validate(); err != nil {
		return 0, true, err
	}
	for _, e := range l.LegacyEntries {
		if e.Path == target {
			return e.Ordinal, true, nil
		}
	}
	for _, a := range l.Assignments {
		if a.State != "aborted" && a.TargetPath == target {
			return a.Ordinal, true, nil
		}
	}
	return 0, true, errors.New("append writer requires a durable assignment")
}

// AssignmentWork 跨准备阶段持OS锁；季锁释放后它仍证明本operation有活跃writer。
type AssignmentWork struct{ file *os.File }

func LockAssignmentWork(a LibraryAssignment) (*AssignmentWork, error) {
	if err := assignmentPlatformCheck(); err != nil {
		return nil, err
	}
	if a.OperationID == "" || a.StagingRoot == "" {
		return nil, errors.New("assignment owner required")
	}
	if err := os.MkdirAll(a.StagingRoot, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(a.StagingRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe assignment staging")
	}
	f, err := assignmentOpenWorkLock(filepath.Join(a.StagingRoot, ".writer.lock"))
	if err != nil {
		return nil, err
	}
	parent, err := os.Open(filepath.Dir(a.StagingRoot))
	if err != nil {
		f.Close()
		return nil, err
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &AssignmentWork{f}, nil
}
func (w *AssignmentWork) Close() {
	if w != nil && w.file != nil {
		assignmentCloseWorkLock(w.file)
		w.file = nil
	}
}

// LockLibraryAssignmentSession 仅锁既有隐藏manifest目录内的同场工作文件。
func LockLibraryAssignmentSession(path string) (*AssignmentWork, error) {
	f, err := assignmentOpenWorkLock(path)
	if err != nil {
		return nil, err
	}
	return &AssignmentWork{file: f}, nil
}

func (s *AssignmentSeason) Prepare(operation string, revision int64, files []AssignmentInstall) error {
	if s.closed || s.ledger.Revision != revision {
		return errors.New("assignment revision changed")
	}
	a, err := s.Find(operation)
	if err != nil {
		return err
	}
	if a.State == "published" {
		return nil
	}
	if a.State != "reserved" && a.State != "committing" {
		return errors.New("assignment cannot prepare")
	}
	if len(files) < 5 {
		return errors.New("complete prepared media and sidecars required")
	}
	media := false
	required := map[string]bool{a.TargetPath: false, sidecarStem(a.TargetPath) + ".srt": false, sidecarStem(a.TargetPath) + ".ass": false, sidecarStem(a.TargetPath) + ".nfo": false, sidecarStem(a.TargetPath) + ".subtitle.json": false, sidecarStem(a.TargetPath) + ".jpg": false}
	seen := map[string]bool{}
	for i := range files {
		f := &files[i]
		if seen[f.Target] {
			return errors.New("duplicate prepared target")
		}
		seen[f.Target] = true
		rel, e := filepath.Rel(a.StagingRoot, f.Staged)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			return errors.New("prepared file escapes owned staging")
		}
		if _, ok := required[f.Target]; ok {
			required[f.Target] = true
		} else if !f.PreserveExisting || (f.Target != filepath.Join(filepath.Dir(s.ledger.SeasonPath), "tvshow.nfo") && f.Target != filepath.Join(filepath.Dir(s.ledger.SeasonPath), "poster.jpg")) {
			return errors.New("unexpected prepared target")
		}
		st, e := AssignmentStat(f.Staged)
		if e != nil {
			return e
		}
		f.Identity = st
		fd, e := os.Open(f.Staged)
		if e != nil {
			return e
		}
		e = fd.Sync()
		fd.Close()
		if e != nil {
			return e
		}
		if f.Target == a.TargetPath {
			media = true
		}
		if current, e := AssignmentStat(f.Target); e == nil {
			if !sameAssignmentFile(current, st) && !f.PreserveExisting {
				return errors.New("prepared target conflicts")
			}
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	for _, ok := range required {
		if !ok {
			return errors.New("prepared result missing member")
		}
	}
	if !media {
		return errors.New("prepared media missing")
	}
	parents := map[string]bool{}
	for _, f := range files {
		parents[filepath.Dir(f.Staged)] = true
	}
	parents[a.StagingRoot] = true
	for parent := range parents {
		dir, e := os.Open(parent)
		if e != nil {
			return e
		}
		e = dir.Sync()
		dir.Close()
		if e != nil {
			return e
		}
	}
	if len(a.Files) > 0 && !reflect.DeepEqual(a.Files, files) {
		return errors.New("prepared files changed")
	}
	a.Files = files
	a.State = "committing"
	return s.Save()
}

// Finish安装短文件；公开媒体最后安装，任何失败不删除已安装的自有文件。
// after-publication重入仅补同operation缺项，不再次聚合，不改变task状态。
func (s *AssignmentSeason) Finish(operation string, revision int64) error {
	if s.closed || s.ledger.Revision != revision {
		return errors.New("assignment revision changed")
	}
	a, err := s.Find(operation)
	if err != nil {
		return err
	}
	if a.State == "published" {
		return s.Validate()
	}
	if a.State != "committing" || len(a.Files) == 0 {
		return errors.New("assignment not prepared")
	}
	for _, f := range a.Files {
		if current, e := AssignmentStat(f.Target); e == nil {
			if !f.PreserveExisting && !sameAssignmentFile(current, f.Identity) {
				return errors.New("owned public target changed")
			}
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	install := func(f AssignmentInstall) error {
		if current, e := AssignmentStat(f.Target); e == nil {
			if f.PreserveExisting || sameAssignmentFile(current, f.Identity) {
				return nil
			}
			return errors.New("owned public target changed")
		} else if !os.IsNotExist(e) {
			return e
		}
		st, e := AssignmentStat(f.Staged)
		if e != nil {
			return e
		}
		if !sameAssignmentFile(st, f.Identity) {
			return errors.New("owned staging changed")
		}
		if e = os.Link(f.Staged, f.Target); e != nil {
			current, ce := AssignmentStat(f.Target)
			if ce != nil || !sameAssignmentFile(current, f.Identity) {
				return e
			}
		}
		d, e := os.Open(filepath.Dir(f.Target))
		if e != nil {
			return e
		}
		e = d.Sync()
		d.Close()
		return e
	}
	for _, f := range a.Files {
		if f.Target != a.TargetPath {
			if err = install(f); err != nil {
				return err
			}
		}
	}
	for _, f := range a.Files {
		if f.Target == a.TargetPath {
			if err = install(f); err != nil {
				return err
			}
		}
	}
	ordinal, _, err := publishedEpisodeMetadata(a.TargetPath, a.Identity)
	if err != nil {
		return err
	}
	if ordinal != a.Ordinal {
		return errors.New("committed NFO differs from assignment")
	}
	now := time.Now().UTC()
	a.PublishedAt = &now
	a.State = "published"
	return s.Save()
}

// ReconcileVisible只核既有公开文件并闭合ledger，绝不安装新文件或推进task。
func (s *AssignmentSeason) ReconcileVisible(operation string, revision int64) error {
	if s.closed || s.ledger.Revision != revision {
		return errors.New("assignment revision changed")
	}
	a, err := s.Find(operation)
	if err != nil {
		return err
	}
	if a.State == "published" {
		return s.Validate()
	}
	if a.State != "committing" || len(a.Files) == 0 {
		return errors.New("visible assignment not prepared")
	}
	conflict := func(reason string) error { return errors.Join(errors.New(reason), s.Block(reason)) }
	media := false
	for _, file := range a.Files {
		current, e := AssignmentStat(file.Target)
		if e != nil || (!file.PreserveExisting && !sameAssignmentFile(current, file.Identity)) {
			return conflict("visible_assignment_file_ownership_conflict")
		}
		if file.Target == a.TargetPath && !file.PreserveExisting {
			media = true
		}
	}
	if !media {
		return conflict("visible_assignment_media_not_owned")
	}
	ordinal, _, err := publishedEpisodeMetadata(a.TargetPath, a.Identity)
	if err != nil || ordinal != a.Ordinal {
		return conflict("visible_assignment_nfo_conflict")
	}
	meta, err := LoadMetadata(sidecarStem(a.TargetPath) + ".subtitle.json")
	if err != nil || meta.Status != StatusCompleted || meta.OutputPath != a.TargetPath || meta.RecordMeta["live_session_id"] != a.SessionID || meta.RecordMeta["live_session_media_role"] != "aggregate" || (a.ContentVersion != "" && meta.RecordMeta["live_session_media_content_hash"] != a.ContentVersion) {
		return conflict("visible_assignment_metadata_conflict")
	}
	now := time.Now().UTC()
	a.PublishedAt = &now
	a.State = "published"
	return s.Save()
}

func (s *AssignmentSeason) Abort(operation string) error {
	a, err := s.Find(operation)
	if err != nil {
		return err
	}
	if a.State == "aborted" {
		return nil
	}
	if a.State == "published" || a.PublishedAt != nil {
		return errors.New("published ordinal is never reclaimed")
	}
	if _, err = os.Lstat(a.TargetPath); !os.IsNotExist(err) {
		return errors.New("visible media ordinal cannot be reclaimed")
	}
	work, err := LockAssignmentWork(*a)
	if err != nil {
		return err
	}
	defer work.Close()
	owned := map[string]AssignmentFileIdentity{}
	for _, file := range a.Files {
		if !file.PreserveExisting {
			owned[file.Target] = file.Identity
		}
	}
	installed := false
	for _, suffix := range []string{".srt", ".ass", ".nfo", ".subtitle.json", ".jpg"} {
		path := sidecarStem(a.TargetPath) + suffix
		current, e := AssignmentStat(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		expected, ok := owned[path]
		if !ok || !sameAssignmentFile(current, expected) {
			cause := errors.New("assignment_sidecar_ownership_conflict")
			return errors.Join(cause, s.Block(cause.Error()))
		}
		installed = true
	}
	if installed {
		// 已安装的侧文件仍由committing清单认领；不丢归属、不删文件或另起operation。
		return ErrLibraryAssignmentPartialInstall
	}
	// 只撤销未公开号，不删除源、attempt或已经可见的孤立sidecar。
	a.State = "aborted"
	if err = s.Save(); err != nil {
		return err
	}
	return nil
}

func (s *AssignmentSeason) Block(reason string) error {
	s.ledger.BlockedReason = reason
	return s.Save()
}

// MergeIndex仅升级已正确确认的原路径；缺失旧保护或身份变化由observer先拒绝。
func (s *AssignmentSeason) MergeIndex(mappings []AssignmentIndexMapping) error {
	changed := false
	for _, m := range mappings {
		for i := range s.ledger.LegacyEntries {
			e := &s.ledger.LegacyEntries[i]
			if e.Path == m.Path {
				if e.Ordinal != m.Ordinal {
					return errors.New("legacy mapping ordinal changed")
				}
				if e.KnownIndexMapping != nil && *e.KnownIndexMapping != m {
					return errors.New("protected mapping changed")
				}
				if e.KnownIndexMapping == nil {
					copy := m
					e.KnownIndexMapping = &copy
					e.IndexState = "protected"
					changed = true
				}
			}
		}
		for i := range s.ledger.Assignments {
			a := &s.ledger.Assignments[i]
			if a.State == "published" && a.TargetPath == m.Path {
				if a.Ordinal != m.Ordinal {
					return errors.New("assignment mapping ordinal changed")
				}
				if a.KnownIndexMapping != nil && *a.KnownIndexMapping != m {
					return errors.New("protected assignment mapping changed")
				}
				if a.KnownIndexMapping == nil {
					copy := m
					a.KnownIndexMapping = &copy
					a.IndexState = "protected"
					changed = true
				}
			}
		}
	}
	if changed {
		return s.Save()
	}
	return nil
}

// BindInputs把reservation绑定到本场可信结果代际，重启不能把旧prepared套到新来源上。
func (s *AssignmentSeason) BindInputs(operation string, revision int64, inputs []AssignmentFileIdentity, version string, sources json.RawMessage) error {
	if s.closed || revision != s.ledger.Revision || len(inputs) == 0 || version == "" || len(sources) == 0 {
		return errors.New("assignment input generation missing")
	}
	a, err := s.Find(operation)
	if err != nil {
		return err
	}
	for _, before := range inputs {
		current, e := AssignmentStat(before.Path)
		if e != nil {
			return e
		}
		if current != before {
			return errors.New("assignment input generation changed")
		}
	}
	if len(a.Inputs) > 0 {
		var before, after bytes.Buffer
		if json.Compact(&before, a.SourceSet) != nil || json.Compact(&after, sources) != nil || !reflect.DeepEqual(a.Inputs, inputs) || a.ContentVersion != version || !bytes.Equal(before.Bytes(), after.Bytes()) {
			return errors.New("reservation input generation changed")
		}
		return nil
	}
	a.Inputs = append([]AssignmentFileIdentity(nil), inputs...)
	a.ContentVersion = version
	a.SourceSet = append(json.RawMessage(nil), sources...)
	return s.Save()
}
func (s *AssignmentSeason) CheckInputs(operation string) error {
	a, err := s.Find(operation)
	if err != nil {
		return err
	}
	if len(a.Inputs) == 0 {
		return errors.New("assignment input generation missing")
	}
	for _, before := range a.Inputs {
		now, e := AssignmentStat(before.Path)
		if e != nil {
			return e
		}
		if now != before {
			return errors.New("assignment input changed before public commit")
		}
	}
	return nil
}
