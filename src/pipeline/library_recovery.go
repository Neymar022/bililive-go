package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/subtitle"
)

type LibraryIndexMapping struct {
	Path, FileID, EpisodeID, ShowID, SeasonID, CategoryID, LibraryID string
	Ordinal                                                          int64
	SeasonNumber                                                     int
}
type LibraryIndexExpected struct {
	OperationID, LibraryRoot, Show, Season, Path, Identity, MediaRevision string
	Ordinal, MediaSize                                                    int64
	PublishedAt                                                           time.Time
	Protected                                                             []LibraryIndexMapping
	Scope                                                                 string                           `json:"scope,omitempty"`
	BaselineLegacyUnconfirmed                                             []LibraryIndexUnconfirmed        `json:"baseline_legacy_unconfirmed,omitempty"`
	OwnedIndexPending                                                     []LibraryIndexUnconfirmed        `json:"owned_index_pending,omitempty"`
	AssignmentOperation                                                   string                           `json:"assignment_operation,omitempty"`
	AssignmentRevision                                                    int64                            `json:"assignment_revision,omitempty"`
	AssignmentSession                                                     string                           `json:"assignment_session,omitempty"`
	ObserveAttempts                                                       int                              `json:"observe_attempts,omitempty"`
	ObserveNotBefore                                                      time.Time                        `json:"observe_not_before,omitempty"`
	LegacyAdmission                                                       *subtitle.LibraryLegacyAdmission `json:"legacy_admission,omitempty"`
}
type LibraryIndexUnconfirmed struct {
	Path, Identity, OperationID string
	Ordinal                     int64
	PublishedAt                 time.Time `json:"published_at,omitempty"`
}
type LibraryIndexObservation struct {
	GeneratedAt                   time.Time
	AdapterVersion, State, Reason string
	Mappings                      []LibraryIndexMapping
	Platform                      subtitle.LibraryRecoveryPlatform
	LegacyUnconfirmed             []LibraryIndexUnconfirmed `json:"legacy_unconfirmed,omitempty"`
	OwnedIndexPending             []LibraryIndexUnconfirmed `json:"owned_index_pending,omitempty"`
}
type LibraryIndexObserver interface {
	VerifyPublication(context.Context, LibraryIndexExpected) (LibraryIndexObservation, error)
}
type LibraryPublicationRequest struct {
	LibraryRoot, Path, SessionID, Phase string
	TaskID                              int64
	RecordedAt                          time.Time
	AppendCommit                        *LibraryAppendCommitRequest
	CommitFiles                         func() error
	ComputeAllowed                      *bool
	ComputeDue                          *time.Time
	IndexObservation                    *LibraryIndexObservation
}

// LibraryRecoveryHooks 只在隔离测试替换时钟/崩溃点；生产没有自动能力豁免。
type LibraryRecoveryHooks struct {
	Now      func() time.Time
	Boundary func(string) error
}

var libraryRecoveryWriter sync.Mutex

func (m *Manager) recoveryStore() (*SQLiteStore, error) {
	s, ok := m.store.(*SQLiteStore)
	if !ok {
		return nil, errors.New("library recovery requires durable SQLite store")
	}
	return s, nil
}
func (m *Manager) recoveryNow() time.Time {
	if m.recoveryHooks.Now != nil {
		return m.recoveryHooks.Now()
	}
	return time.Now()
}
func (m *Manager) recoveryBoundary(name string) error {
	if m.recoveryHooks.Boundary != nil {
		return m.recoveryHooks.Boundary(name)
	}
	return nil
}
func (m *Manager) SetLibraryIndexObserver(observer LibraryIndexObserver) { m.indexObserver = observer }
func (m *Manager) SetLibraryRecoveryHooks(hooks LibraryRecoveryHooks)    { m.recoveryHooks = hooks }

// EnsureLibraryPublication 接在真实发布锁内、concat 前；重入先验证持久 before/after。
func (m *Manager) EnsureLibraryPublication(ctx context.Context, req LibraryPublicationRequest) error {
	s, err := m.recoveryStore()
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	season := filepath.Dir(req.Path)
	records, err := s.libraryRecoveries(ctx)
	if err != nil {
		return err
	}
	// append分流也必须经过B坏journal保护；坏control只关闭producer，不阻保存结果领取。
	for _, candidate := range records {
		if candidate.InvalidJournal && (candidate.Season == season || req.TaskID > 0 && candidate.TaskID == req.TaskID) {
			return errors.New("journal_invalid: affected append owner/season protected")
		}
	}
	if req.Phase == "append_capability" || req.Phase == "append_snapshot" || req.Phase == "append_commit" || req.Phase == "append_compute_permission" || req.Phase == "append_compute_required" {
		return m.libraryAppendCommit(ctx, req)
	}
	var r *LibraryRecoveryRecord
	for _, candidate := range records {
		if candidate.InvalidJournal && candidate.Season == season {
			return errors.New("journal_invalid: affected season protected")
		}
		if req.Phase != "published" && candidate.Season == season && candidate.State == "external_index_conflict" {
			return errors.New("external_index_conflict: " + candidate.Reason)
		}
		if candidate.Kind == "files" && candidate.Season == season && candidate.Active {
			r = candidate
			break
		}
	}
	if req.Phase == "published" {
		if enabled, e := subtitle.LibraryAppendEnabled(season); e != nil {
			return e
		} else if enabled {
			return m.observeAppendPublication(ctx, req, records)
		}
		return m.observeLibraryPublication(ctx, req, records)
	}
	markerPath := subtitle.LibraryRecoveryMarker(req.Path)
	if info, err := os.Lstat(markerPath); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("recovery_marker_conflict")
		}
		marker, err := os.ReadFile(markerPath)
		if err != nil {
			return err
		}
		if r == nil || string(marker) != r.OperationID {
			return errors.New("recovery_marker_conflict")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if req.TaskID > 0 {
		if task, e := m.store.GetTask(ctx, req.TaskID); e != nil {
			return e
		} else if task.Status == PipelineStatusCancelled {
			return context.Canceled
		}
	}
	for _, queued := range records {
		if queued.Kind == "task" && queued.TaskID == req.TaskID {
			if err = validateRecoverySources(queued); err != nil {
				return err
			}
		}
	}
	if err = m.libraryPublicationTurn(ctx, req); err != nil {
		return err
	}
	if enabled, e := subtitle.LibraryAppendEnabled(season); e != nil {
		return e
	} else if enabled {
		// append季禁止落入旧的按录制时间修复/重排流程。
		_, e = subtitle.BuildLibraryEpisodeNFO(req.Path, req.RecordedAt, "")
		return e
	}
	if r != nil && r.State == "external_index_conflict" {
		return fmt.Errorf("external_index_conflict: %s", r.Reason)
	}
	if r != nil && r.State == "blocked" {
		fingerprint := libraryEvidenceFingerprint(season)
		if !canReclassifyLibraryBlock(r, fingerprint) {
			return fmt.Errorf("library recovery blocked: %s", r.Reason)
		}
		if r.Plan != nil {
			if err = removeOwnedLibraryRecoveryMarker(r); err != nil {
				return err
			}
		}
		r.Active = false
		if err = s.saveLibraryRecovery(ctx, r); err != nil {
			return err
		}
		r = nil
	}
	if r == nil {
		// 正常健康季和非 Linux 正常发布仍沿用原 validator，不强制生成恢复记录。
		if _, err = subtitle.BuildLibraryEpisodeNFO(req.Path, req.RecordedAt, ""); err == nil {
			for _, queued := range records {
				if queued.Kind == "task" && queued.TaskID == req.TaskID {
					if err = m.captureLibraryBaseline(ctx, s, queued); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if !subtitle.LibraryRecoverySupported() {
			return &subtitle.LibraryRecoveryError{Reason: "capability_unavailable_non_linux", Path: season}
		}
		plan, err := subtitle.PlanLibraryIdentityRecovery(season)
		if err != nil {
			return m.recordLibraryBlock(ctx, s, season, req.TaskID, err)
		}
		if len(plan.Members) > 0 {
			last := plan.Members[len(plan.Members)-1]
			existing := false
			for _, member := range plan.Members {
				if member.Media.Path == req.Path {
					existing = true
				}
			}
			if !existing && !req.RecordedAt.After(last.RecordedAt) {
				return m.recordLibraryBlock(ctx, s, season, req.TaskID, errors.New("ordinal_repair_required: historical insertion"))
			}
		}
		changed := false
		for _, member := range plan.Members {
			if !bytes.Equal(member.Before, member.After) {
				changed = true
			}
		}
		if !changed {
			return m.recordLibraryBlock(ctx, s, season, req.TaskID, errors.New("ordinal_repair_required: new publication is not append-only"))
		}
		r = &LibraryRecoveryRecord{OperationID: plan.Hash(), Kind: "files", Season: season, TaskID: req.TaskID, State: "detected", Active: true, Plan: plan, PlanHash: plan.Hash()}
		r.Expected = LibraryIndexExpected{OperationID: r.OperationID, LibraryRoot: req.LibraryRoot, Show: filepath.Base(filepath.Dir(season)), Season: season, Path: req.Path}
		if err = s.saveLibraryRecovery(ctx, r); err != nil {
			return err
		}
		if err = m.recoveryBoundary("detected"); err != nil {
			return err
		}
	}
	if err = m.captureLibraryBaseline(ctx, s, r); err != nil {
		return err
	}
	for _, queued := range records {
		if queued.Kind == "task" && queued.TaskID == req.TaskID && !queued.BeforeCaptured {
			queued.Expected = r.Expected
			queued.Expected.OperationID = queued.OperationID
			queued.BeforeCaptured = true
			if err = s.saveLibraryRecovery(ctx, queued); err != nil {
				return err
			}
		}
	}
	return m.applyLibraryRecovery(ctx, s, r, req.LibraryRoot)
}
func (m *Manager) recordLibraryBlock(ctx context.Context, s *SQLiteStore, season string, taskID int64, cause error) error {
	r := &LibraryRecoveryRecord{OperationID: "blocked-" + subtitle.LibraryRecoveryDigest([]byte(season+libraryEvidenceFingerprint(season)+cause.Error())), Fingerprint: libraryEvidenceFingerprint(season), Kind: "files", Season: season, TaskID: taskID, State: "blocked", Reason: cause.Error(), Active: true}
	if err := s.saveLibraryRecovery(ctx, r); err != nil {
		return fmt.Errorf("%w; persist block: %v", cause, err)
	}
	return cause
}
func (m *Manager) applyLibraryRecovery(ctx context.Context, s *SQLiteStore, r *LibraryRecoveryRecord, library string) error {
	libraryRecoveryWriter.Lock()
	defer libraryRecoveryWriter.Unlock()
	unlock, lockErr := subtitle.LockLibraryRecoveryWriter(ctx, library)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	if err := m.confirmLibraryWriterContract(ctx, r); err != nil {
		return err
	}
	if r.Plan == nil {
		return errors.New("missing recovery plan")
	}
	fail := func(cause error) error {
		r.State = "blocked"
		r.Reason = cause.Error()
		r.Fingerprint = libraryEvidenceFingerprint(r.Season)
		_ = s.saveLibraryRecovery(context.WithoutCancel(ctx), r)
		return cause
	}
	if err := subtitle.ValidateLibraryIdentityPlan(r.Plan); err != nil {
		return fail(err)
	}
	if stopped, err := m.stopCancelledLibraryRecovery(ctx, s, r); err != nil {
		return err
	} else if stopped {
		return context.Canceled
	}
	marker := subtitle.LibraryRecoveryMarker(filepath.Join(r.Season, ".recovery"))
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(marker); err == nil && !info.Mode().IsRegular() {
		return fail(errors.New("recovery_marker_conflict"))
	}
	if content, err := os.ReadFile(marker); os.IsNotExist(err) {
		f, e := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = f.WriteString(r.OperationID)
		if e == nil {
			e = f.Sync()
		}
		f.Close()
		if e != nil {
			return e
		}
		if e = subtitle.SyncLibraryRecoveryDirectory(filepath.Dir(marker)); e != nil {
			return e
		}
	} else if err != nil || string(content) != r.OperationID {
		return fail(errors.New("recovery_marker_conflict"))
	}
	var backupBytes uint64
	for _, member := range r.Plan.Members {
		backupBytes += uint64(len(member.Before)+len(member.After)) * 2
	}
	if err := subtitle.CheckLibraryRecoverySpace(library, backupBytes+1024*1024); err != nil {
		return err
	}
	root := filepath.Join(subtitle.LibraryRecoveryRoot(library), "operations", r.OperationID)
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	if err := subtitle.SyncLibraryRecoveryDirectory(filepath.Dir(root)); err != nil {
		return err
	}
	// 全部独立备份/after 先落盘。恢复时只认已知内容与属性，不重建坏备份。
	for i, member := range r.Plan.Members {
		if bytes.Equal(member.Before, member.After) {
			continue
		}
		for _, variant := range []struct {
			name string
			data []byte
		}{{"before", member.Before}, {"after", member.After}} {
			path := filepath.Join(root, fmt.Sprintf("%04d.%s", i, variant.name))
			state, err := subtitle.CaptureLibraryFileState(path, true)
			if os.IsNotExist(err) {
				if r.State != "detected" {
					return fail(errors.New("backup_missing: " + path))
				}
				if err = subtitle.WriteLibraryRecoveryCopy(path, variant.data, member.NFO); err != nil {
					return fail(err)
				}
			} else if err != nil {
				return fail(err)
			} else if state.SHA != subtitle.LibraryRecoveryDigest(variant.data) || state.UID != member.NFO.UID || state.GID != member.NFO.GID || state.Mode != member.NFO.Mode || state.MtimeNS != member.NFO.MtimeNS || !equalJSON(state.Xattrs, member.NFO.Xattrs) {
				return fail(errors.New("backup_corrupt: " + path))
			}
			if err = m.recoveryBoundary(fmt.Sprintf("backup_%d_%s", i, variant.name)); err != nil {
				return err
			}
		}
	}
	if r.State == "detected" {
		r.State = "prepared"
		if err := s.saveLibraryRecovery(ctx, r); err != nil {
			return err
		}
		if err := m.recoveryBoundary("prepared"); err != nil {
			return err
		}
	}
	for i, member := range r.Plan.Members {
		if bytes.Equal(member.Before, member.After) {
			continue
		}
		if err := subtitle.ValidateLibraryIdentityPlan(r.Plan); err != nil {
			return fail(err)
		}
		current, err := os.ReadFile(member.NFO.Path)
		if err != nil {
			return fail(err)
		}
		if bytes.Equal(current, member.After) {
			continue
		}
		r.State = "applying"
		r.Reason = member.NFO.Path
		if err = s.saveLibraryRecovery(ctx, r); err != nil {
			return err
		}
		if err = m.recoveryBoundary(fmt.Sprintf("intent_%d", i)); err != nil {
			return err
		}
		// staging 与目标同目录以保持继承；私有 after 副本永久保留供重入验证。
		staged := filepath.Join(r.Plan.Season, ".identity-"+r.OperationID+"-"+strconv.Itoa(i))
		if _, err = os.Lstat(staged); os.IsNotExist(err) {
			err = subtitle.WriteLibraryRecoveryCopy(staged, member.After, member.NFO)
		} else if err == nil {
			state, e := subtitle.CaptureLibraryFileState(staged, true)
			if e != nil || state.SHA != subtitle.LibraryRecoveryDigest(member.After) || state.UID != member.NFO.UID || state.GID != member.NFO.GID || state.Mode != member.NFO.Mode || state.MtimeNS != member.NFO.MtimeNS || !equalJSON(state.Xattrs, member.NFO.Xattrs) {
				return fail(errors.New("staged_third_value_or_attributes: " + staged))
			}
		}
		if err != nil {
			return fail(err)
		}
		if err = subtitle.ValidateLibraryIdentityPlan(r.Plan); err != nil {
			return fail(err)
		}
		if err = m.confirmLibraryWriterContract(ctx, r); err != nil {
			return err
		}
		if err = m.recoveryBoundary(fmt.Sprintf("before_rename_%d", i)); err != nil {
			return err
		}
		// 在首笔公开写入前重新读取持久取消；已有 after 时仅完成原计划一致性。
		if stopped, err := m.stopCancelledLibraryRecovery(ctx, s, r); err != nil {
			return err
		} else if stopped {
			return context.Canceled
		}
		if err = os.Rename(staged, member.NFO.Path); err != nil {
			return fail(err)
		}
		if err = m.recoveryBoundary(fmt.Sprintf("rename_%d", i)); err != nil {
			return err
		}
		if cancelled, err := m.libraryRecoveryTaskCancelled(ctx, r); err != nil {
			return err
		} else if cancelled {
			ctx = context.WithoutCancel(ctx)
		}
		if err = subtitle.SyncLibraryRecoveryDirectory(r.Plan.Season); err != nil {
			return fail(err)
		}
		if err = m.recoveryBoundary(fmt.Sprintf("fsync_%d", i)); err != nil {
			return err
		}
	}
	if err := subtitle.ValidateLibraryIdentityPlan(r.Plan); err != nil {
		return fail(err)
	}
	for _, member := range r.Plan.Members {
		if err := subtitle.ValidatePublishedLibraryEpisode(member.Media.Path); err != nil {
			return fail(err)
		}
	}
	r.State = "nfo_verified"
	r.Reason = ""
	if err := s.saveLibraryRecovery(ctx, r); err != nil {
		return err
	}
	if err := m.recoveryBoundary("nfo_verified"); err != nil {
		return err
	}
	if cancelled, err := m.libraryRecoveryTaskCancelled(ctx, r); err != nil {
		return err
	} else if cancelled {
		r.State, r.Active = "cancelled", false
		if err := s.saveLibraryRecovery(context.WithoutCancel(ctx), r); err != nil {
			return err
		}
		if err := removeOwnedLibraryRecoveryMarker(r); err != nil {
			return err
		}
		return context.Canceled
	}
	return nil
}

// 新证据只唤醒尚未改写的拒绝；部分 after 或第三值不得成为新计划的基线。
func canReclassifyLibraryBlock(r *LibraryRecoveryRecord, fingerprint string) bool {
	if r.State != "blocked" || r.Fingerprint == fingerprint {
		return false
	}
	if r.Plan != nil {
		for _, member := range r.Plan.Members {
			current, err := os.ReadFile(member.NFO.Path)
			if err != nil || !bytes.Equal(current, member.Before) {
				return false
			}
		}
	}
	return true
}

func (m *Manager) stopCancelledLibraryRecovery(ctx context.Context, s *SQLiteStore, r *LibraryRecoveryRecord) (bool, error) {
	cancelled, err := m.libraryRecoveryTaskCancelled(ctx, r)
	if err != nil || !cancelled {
		return false, err
	}
	for _, member := range r.Plan.Members {
		if bytes.Equal(member.Before, member.After) {
			continue
		}
		current, err := os.ReadFile(member.NFO.Path)
		if err != nil {
			return false, err
		}
		if bytes.Equal(current, member.After) {
			return false, nil
		}
		if !bytes.Equal(current, member.Before) {
			return false, errors.New("cancelled_recovery_third_value: " + member.NFO.Path)
		}
	}
	r.State, r.Active, r.Reason = "cancelled", false, "cancelled_before_first_write"
	if err := s.saveLibraryRecovery(context.WithoutCancel(ctx), r); err != nil {
		return false, err
	}
	return true, removeOwnedLibraryRecoveryMarker(r)
}

func (m *Manager) libraryRecoveryTaskCancelled(ctx context.Context, r *LibraryRecoveryRecord) (bool, error) {
	if r.TaskID == 0 {
		return false, nil
	}
	task, err := m.store.GetTask(context.WithoutCancel(ctx), r.TaskID)
	if err != nil {
		return false, err
	}
	return task.Status == PipelineStatusCancelled, nil
}

// 调用者持有 Season 锁；陌生、链接或不属于本 operation 的 marker 一律保留。
func removeOwnedLibraryRecoveryMarker(r *LibraryRecoveryRecord) error {
	marker := subtitle.LibraryRecoveryMarker(filepath.Join(r.Season, ".recovery"))
	info, err := os.Lstat(marker)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("recovery_marker_conflict")
	}
	content, err := os.ReadFile(marker)
	if err != nil {
		return err
	}
	if string(content) != r.OperationID {
		return errors.New("recovery_marker_conflict")
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	return subtitle.SyncLibraryRecoveryDirectory(filepath.Dir(marker))
}
func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func (m *Manager) observeLibraryPublication(ctx context.Context, req LibraryPublicationRequest, records []*LibraryRecoveryRecord) error {
	for _, r := range records {
		if r.InvalidJournal || r.Kind == "control" {
			continue
		}
		if r.Season != filepath.Dir(req.Path) || r.State == "blocked" {
			continue
		}
		if r.Kind == "task" && r.TaskID != req.TaskID {
			continue
		}
		if r.Kind == "files" && !r.Active && r.State != "published" {
			continue
		}
		state, err := subtitle.CaptureLibraryFileState(req.Path, false)
		if err != nil {
			return err
		}
		r.Expected.Path = req.Path
		r.Expected.Ordinal, _ = subtitle.PublishedLibraryOrdinal(req.Path)
		r.Expected.Identity = libraryPathIdentity(req.Path)
		r.Expected.MediaSize = state.Size
		r.Expected.MediaRevision = fmt.Sprintf("%d:%d:%d:%d", state.Device, state.Inode, state.Size, state.MtimeNS)
		if r.Expected.PublishedAt.IsZero() {
			r.Expected.PublishedAt = m.recoveryNow()
		}
		r.State = "published"
		r.Active = false
		if err = safePublished(req.Path); err != nil {
			return err
		}
		if err = sMustSave(m, ctx, r); err != nil {
			return err
		}
		if err = m.recoveryBoundary("published"); err != nil {
			return err
		}
		observation := LibraryIndexObservation{State: "index_unconfirmed", Reason: "observer_not_configured"}
		if m.indexObserver != nil && r.BeforeCaptured {
			observation, err = m.indexObserver.VerifyPublication(ctx, r.Expected)
			if err != nil {
				observation.State = "index_unconfirmed"
				observation.Reason = err.Error()
			}
		}
		after, checkErr := subtitle.CaptureLibraryFileState(req.Path, false)
		if checkErr != nil || !equalJSON(state, after) {
			observation.State = "external_index_conflict"
			observation.Reason = "publication_media_changed_during_observation"
		}
		if observation.State != "verified" && observation.State != "external_index_conflict" {
			observation.State = "index_unconfirmed"
		}
		r.Observation = observation
		r.State = observation.State
		if r.State == "external_index_conflict" {
			r.Active = r.Kind == "files"
			r.Reason = observation.Reason
			if err = freezeRecoverySeason(r); err != nil {
				return err
			}
		}
		if err = sMustSave(m, ctx, r); err != nil {
			return err
		}
		if r.Kind == "files" && r.State != "external_index_conflict" {
			if err = m.recoveryBoundary("observation_committed"); err != nil {
				return err
			}
			if err = removeOwnedLibraryRecoveryMarker(r); err != nil {
				return err
			}
		}
		if r.State == "external_index_conflict" {
			return errors.New("external_index_conflict: " + r.Reason)
		}
	}
	return nil
}
func safePublished(path string) error { return subtitle.ValidatePublishedLibraryEpisode(path) }
func sMustSave(m *Manager, ctx context.Context, r *LibraryRecoveryRecord) error {
	s, err := m.recoveryStore()
	if err != nil {
		return err
	}
	return s.saveLibraryRecovery(ctx, r)
}
func libraryPathIdentity(path string) string {
	base := filepath.Base(path)
	start := strings.Index(base, ".S01E")
	if start < 0 {
		return ""
	}
	rest := base[start+5:]
	end := strings.Index(rest, ".")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func (m *Manager) libraryPublicationTurn(ctx context.Context, req LibraryPublicationRequest) error {
	if enabled, err := subtitle.LibraryAppendEnabled(filepath.Dir(req.Path)); err != nil {
		return err
	} else if enabled {
		rs, err := m.recordingStore()
		if err != nil {
			return err
		}
		session, err := rs.RecordingSession(ctx, req.SessionID)
		if err != nil {
			return err
		}
		if !session.Ready() {
			var ids []string
			for id, member := range session.Tasks {
				if !member.Ready {
					ids = append(ids, id)
				}
			}
			sort.Strings(ids)
			return NewRetryLaterError(fmt.Errorf("session_incomplete blocking_members=%s", strings.Join(ids, ",")), 5*time.Minute)
		}
		ledger, err := subtitle.LoadLibraryAssignments(filepath.Dir(req.Path))
		if err != nil {
			return err
		}
		for _, e := range ledger.LegacyEntries {
			if e.Path == req.Path {
				return nil
			}
		}
		for _, a := range ledger.Assignments {
			if a.State != "aborted" && a.SessionID == req.SessionID && a.TargetPath == req.Path {
				return nil
			}
		}
		return errors.New("append_assignment_required")
	}
	tasks, err := m.store.ListTasks(ctx, TaskFilter{})
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.ID == req.TaskID || task.RecordInfo.LiveSessionID == "" || task.RecordInfo.LiveSessionID == req.SessionID || task.Status == PipelineStatusCompleted || task.Status == PipelineStatusCancelled {
			continue
		}
		if m.taskLibrarySeason(task, req.LibraryRoot) != filepath.Dir(req.Path) {
			continue
		}
		if task.RecordInfo.StartTime.IsZero() {
			return errors.New("earlier_session_identity_unknown")
		}
		if task.RecordInfo.StartTime.Before(req.RecordedAt) || (task.RecordInfo.StartTime.Equal(req.RecordedAt) && task.RecordInfo.LiveSessionID < req.SessionID) {
			return NewRetryLaterError(errors.New("waiting_for_earlier_season_session"), 5*time.Minute)
		}
	}
	return nil
}

// recoverLibraryJournals 在调度放行前恢复已知写入，不恢复被取消的任务。
func (m *Manager) recoverLibraryJournals() error {
	s, err := m.recoveryStore()
	if err != nil {
		return nil
	}
	records, err := s.libraryRecoveries(m.ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, r := range records {
		if r.InvalidJournal || r.Kind != "files" || r.State == "blocked" || r.State == "external_index_conflict" || r.Plan == nil {
			continue
		}
		if !r.Active && r.State != "published" {
			if r.State != "index_unconfirmed" && r.State != "verified" && r.State != "cancelled" {
				continue
			}
			if _, err := os.Lstat(subtitle.LibraryRecoveryMarker(filepath.Join(r.Season, ".recovery"))); os.IsNotExist(err) {
				continue
			}
		}
		library := filepath.Dir(filepath.Dir(r.Season))
		unlock, err := subtitle.LockLibraryPublicationContext(m.ctx, filepath.Join(r.Season, ".recovery"))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// 崩溃可能在成品提交后、journal回执前；只认完整真实成品，不重复合并。
		if !r.Active && r.State != "published" {
			err = reconcileTerminalLibraryRecovery(r)
		} else if r.Expected.Path != "" && completeRecoveryPublication(r.Expected.Path) == nil {
			err = subtitle.ValidateLibraryIdentityPlan(r.Plan, r.Expected.Path)
			if err == nil {
				err = m.observeLibraryPublication(m.ctx, LibraryPublicationRequest{LibraryRoot: library, Path: r.Expected.Path, TaskID: r.TaskID, Phase: "published"}, []*LibraryRecoveryRecord{r})
			}
		} else {
			err = m.captureLibraryBaseline(m.ctx, s, r)
			if err == nil {
				err = m.applyLibraryRecovery(m.ctx, s, r, library)
				if errors.Is(err, context.Canceled) && r.State == "cancelled" {
					err = nil
				}
			}
			if err == nil {
				if task, e := m.store.GetTask(m.ctx, r.TaskID); e == nil && task.Status == PipelineStatusCancelled {
					r.State = "cancelled"
					r.Active = false
					err = s.saveLibraryRecovery(m.ctx, r)
					if err == nil {
						err = removeOwnedLibraryRecoveryMarker(r)
					}
				}
			}
		}
		unlock()
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func reconcileTerminalLibraryRecovery(r *LibraryRecoveryRecord) error {
	if r.State == "cancelled" {
		if err := subtitle.ValidateLibraryIdentityPlan(r.Plan); err != nil {
			return err
		}
	} else {
		if err := completeRecoveryPublication(r.Expected.Path); err != nil {
			return err
		}
		if err := subtitle.ValidateLibraryIdentityPlan(r.Plan, r.Expected.Path); err != nil {
			return err
		}
		state, err := subtitle.CaptureLibraryFileState(r.Expected.Path, false)
		if err != nil {
			return err
		}
		if r.Expected.MediaRevision != fmt.Sprintf("%d:%d:%d:%d", state.Device, state.Inode, state.Size, state.MtimeNS) {
			return errors.New("published_media_revision_changed")
		}
	}
	return removeOwnedLibraryRecoveryMarker(r)
}

// compensateLibraryFailures 每次 poll 至多提交一个候选，不增加常驻协程。
func (m *Manager) compensateLibraryFailures() {
	s, err := m.recoveryStore()
	if err != nil {
		return
	}
	m.refreshLibraryIndex(m.ctx, s)
	records, err := s.libraryRecoveries(m.ctx)
	if err != nil {
		return
	}
	byTask := map[int64]*LibraryRecoveryRecord{}
	for _, r := range records {
		if r.Kind == "task" {
			byTask[r.TaskID] = r
		}
	}
	status := PipelineStatusFailed
	tasks, err := m.store.ListTasks(m.ctx, TaskFilter{Status: &status})
	if err != nil {
		return
	}
	sort.Slice(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		if a.RecordInfo.StartTime.Equal(b.RecordInfo.StartTime) {
			return a.ID < b.ID
		}
		return a.RecordInfo.StartTime.Before(b.RecordInfo.StartTime)
	})
	cfg := configs.GetCurrentConfig()
	if cfg == nil || !cfg.Subtitle.Enabled {
		return
	}
	library := cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath)
	library, _ = filepath.Abs(library)
	now := m.recoveryNow()
	for _, task := range tasks {
		if old := byTask[task.ID]; old != nil && (old.InvalidJournal || old.RecoveryClass == "worker_completion" || old.RecoveryClass == "deadline" || old.RecoveryClass == "completed_media") {
			continue
		}
		if !task.CanRetry || task.RecordInfo.LiveSessionID == "" || task.RecordInfo.RecordingProducerID == "" || task.CurrentStage < 0 || task.PipelineConfig == nil || task.CurrentStage >= len(task.PipelineConfig.Stages) || task.PipelineConfig.Stages[task.CurrentStage].Name != StageNameSubtitleGenerate {
			continue
		}
		appendMode, appendErr := subtitle.LibraryAppendEnabled(m.taskLibrarySeason(task, library))
		if appendErr != nil {
			continue
		}
		if !appendMode && byTask[task.ID] == nil && !strings.Contains(task.ErrorMessage, "NFO") && !strings.Contains(task.ErrorMessage, "nfo_") && !strings.Contains(task.ErrorMessage, "ordinal") {
			continue
		}
		rs, e := m.recordingStore()
		if e != nil {
			continue
		}
		session, e := rs.RecordingSession(m.ctx, task.RecordInfo.LiveSessionID)
		if e != nil || !session.Ready() {
			continue
		}
		if e = m.libraryCompletedCheckpoint(task, session); e != nil {
			continue
		}
		if e = m.validateTaskResume(task); e != nil {
			continue
		}
		r := byTask[task.ID]
		if r == nil {
			due := now
			if cfg.Subtitle.Schedule.Enabled && !appendMode {
				due, e = cfg.Subtitle.Schedule.NextRunAfter(now)
				if e != nil {
					continue
				}
			}
			if task.NotBefore != nil && task.NotBefore.After(due) {
				due = *task.NotBefore
			}
			r = &LibraryRecoveryRecord{OperationID: fmt.Sprintf("task-%d", task.ID), Kind: "task", Season: m.taskLibrarySeason(task, library), TaskID: task.ID, State: "waiting", NextAttempt: due, Expected: LibraryIndexExpected{LibraryRoot: library, Show: filepath.Base(filepath.Dir(m.taskLibrarySeason(task, library))), Season: m.taskLibrarySeason(task, library)}}
			if subtitle.LibraryRecoverySupported() {
				for _, source := range session.Sources() {
					state, err := subtitle.CaptureLibraryFileState(source.InputPath, false)
					if os.IsNotExist(err) {
						// 仅接受已有正式发布证明的缺失原片，不把普通缺源解释为已清理。
						err = m.publishedRecoveryTurn(task.ID, session, byTask)
						if err == nil {
							err = m.protectPublishedMissingSource(r, session.ID, source)
						}
						if err == nil {
							continue
						}
					}
					if err != nil {
						r.Reason = "source_preservation_unavailable: " + source.InputPath
						break
					}
					r.ProtectedSources = append(r.ProtectedSources, state)
				}
				if r.Reason != "" {
					continue
				}
			}
			if e = s.saveLibraryRecovery(m.ctx, r); e != nil {
				continue
			}
			return
		}
		if r.State == "resumed" || r.State == "blocked" || r.State == "published" || r.State == "index_unconfirmed" {
			if appendMode {
				// append不再通过旧时间序NFO fingerprint解封；ledger真实冲突保持该season隔离。
				ledger, err := subtitle.LoadLibraryAssignments(r.Season)
				if err != nil || ledger.BlockedReason != "" {
					continue
				}
				r.State = "waiting"
				r.Reason = task.ErrorMessage
				r.NextAttempt = now.Add(recoveryBackoff(r.Attempts))
				if task.NotBefore != nil && task.NotBefore.After(r.NextAttempt) {
					r.NextAttempt = *task.NotBefore
				}
				_ = s.saveLibraryRecovery(m.ctx, r)
				return
			}
			fingerprint := libraryEvidenceFingerprint(r.Season)
			hard := false
			for _, fileRecovery := range records {
				if fileRecovery.Kind == "files" && fileRecovery.Season == r.Season && fileRecovery.Active && (fileRecovery.State == "blocked" || fileRecovery.State == "external_index_conflict") {
					hard = !canReclassifyLibraryBlock(fileRecovery, fingerprint)
				}
			}
			if hard && r.State == "blocked" && r.Fingerprint == fingerprint {
				continue
			}
			r.Fingerprint = fingerprint
			r.Reason = task.ErrorMessage
			if hard {
				r.State = "blocked"
			} else {
				r.State = "waiting"
				r.NextAttempt = now.Add(recoveryBackoff(r.Attempts))
				if cfg.Subtitle.Schedule.Enabled && !appendMode {
					if due, e := cfg.Subtitle.Schedule.NextRunAfter(r.NextAttempt); e == nil {
						r.NextAttempt = due
					}
				}
				if task.NotBefore != nil && task.NotBefore.After(r.NextAttempt) {
					r.NextAttempt = *task.NotBefore
				}
			}
			_ = s.saveLibraryRecovery(m.ctx, r)
			return
		}
		if (r.State != "waiting" && r.State != "resume_requested") || now.Before(r.NextAttempt) {
			continue
		}
		r.State = "resume_requested"
		r.Attempts++
		if e = s.saveLibraryRecovery(m.ctx, r); e != nil {
			return
		}
		if e = m.recoveryBoundary("resume_requested"); e != nil {
			return
		}
		var required uint64
		for _, source := range session.Sources() {
			if info, e := os.Stat(source.LibraryPath); e == nil {
				required += uint64(info.Size()) * 2
			}
		}
		if subtitle.LibraryRecoverySupported() {
			e = subtitle.CheckLibraryRecoverySpace(library, required+1024*1024)
		}
		if e == nil {
			e = validateRecoverySources(r)
		}
		if e == nil {
			e = m.ResumeTask(task.ID)
		}
		if e != nil {
			r.State = "waiting"
			r.Reason = e.Error()
			r.NextAttempt = now.Add(recoveryBackoff(r.Attempts))
			if cfg.Subtitle.Schedule.Enabled && !appendMode {
				if due, e := cfg.Subtitle.Schedule.NextRunAfter(r.NextAttempt); e == nil && due.After(r.NextAttempt) {
					r.NextAttempt = due
				}
			}
			_ = s.saveLibraryRecovery(m.ctx, r)
		}
		return
	}
}
func recoveryBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return 5 * time.Minute
	}
	if attempt == 2 {
		return 15 * time.Minute
	}
	if attempt == 3 {
		return time.Hour
	}
	return 6 * time.Hour
}

func (m *Manager) libraryTaskRecovery(id int64) *LibraryRecoveryRecord {
	s, e := m.recoveryStore()
	if e != nil {
		return nil
	}
	records, e := s.libraryRecoveries(m.ctx)
	if e != nil {
		return nil
	}
	var found *LibraryRecoveryRecord
	for _, r := range records {
		if r.TaskID == id {
			found = r
		}
	}
	return found
}
func (m *Manager) librarySourcesProtected(id int64) bool {
	s, err := m.recoveryStore()
	if err != nil {
		return true
	}
	records, err := s.libraryRecoveries(m.ctx)
	if err != nil {
		return true
	}
	for _, r := range records {
		if r.TaskID == id {
			return true
		}
	}
	return false
}

// 只有普通调度的 deadline 记录不代表已经开始了字幕恢复。
func (r *LibraryRecoveryRecord) requiresSubtitleRecovery() bool {
	return r.Kind == "files" && r.Active || r.Kind == "task" &&
		(r.RecoveryClass != "deadline" || r.State != "deadline" || len(r.CompletionRefs) != 0 ||
			len(r.CompletedSources) != 0 || r.AggregateCompute != nil)
}

func (m *Manager) subtitleRecoveryRequired(id int64) (bool, error) {
	store, ok := m.store.(*SQLiteStore)
	if !ok {
		return false, nil
	}
	records, err := store.libraryRecoveries(m.ctx)
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if r.TaskID == id && r.requiresSubtitleRecovery() {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) libraryTaskCanStart(task *PipelineTask) bool {
	cfg := configs.GetCurrentConfig()
	if s, ok := m.store.(*SQLiteStore); ok {
		records, e := s.libraryRecoveries(m.ctx)
		if e != nil {
			return false
		}
		for _, r := range records {
			if (cfg == nil || !cfg.Subtitle.Enabled) && r.TaskID == task.ID && r.requiresSubtitleRecovery() {
				return false
			}
			if r.InvalidJournal && (r.TaskID == task.ID || r.Season != "" && (cfg == nil || r.Season == m.taskLibrarySeason(task, cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath)))) {
				return false
			}
		}
	}

	if r, e := m.completionRecord(task.ID); e == nil && len(r.CompletionRefs) > 0 {
		for _, ref := range r.CompletionRefs {
			if ref.State != "accepted" && ref.State != "queued_unsent" && ref.State != "superseded" {
				return false
			}
		}
	}
	if task.RecordInfo.LiveSessionID == "" {
		return true
	}
	m.mu.RLock()
	var ids []int64
	for id := range m.runningTasks {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	for _, id := range ids {
		running, err := m.store.GetTask(m.ctx, id)
		if err == nil && m.sameLibraryTaskSeason(running, task) {
			return false
		}
	}
	return true
}

func (m *Manager) libraryCompletedCheckpoint(task *PipelineTask, session RecordingSession) error {
	found := false
	for _, source := range session.Sources() {
		meta, err := subtitle.LoadMetadata(source.MetadataPath)
		if err != nil {
			return err
		}
		stem := strings.TrimSuffix(source.LibraryPath, filepath.Ext(source.LibraryPath))
		if meta.Status != subtitle.StatusCompleted || len(meta.Segments) == 0 || meta.SourcePath != source.InputPath || meta.OutputPath != source.LibraryPath || meta.SRTPath != stem+".srt" || meta.ASSPath != stem+".ass" || meta.RecordMeta["live_session_id"] != session.ID || meta.RecordMeta["recording_producer_id"] != source.ProducerID || meta.RecordMeta["pipeline_task_id"] != strconv.FormatInt(source.TaskID, 10) {
			return errors.New("completed checkpoint ownership mismatch")
		}
		for _, path := range []string{source.LibraryPath, meta.SRTPath, meta.ASSPath} {
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() == 0 {
				return errors.New("completed checkpoint unavailable")
			}
		}
		if source.TaskID == task.ID {
			found = true
		}
	}
	if !found {
		return errors.New("task has no complete recording source")
	}
	return nil
}

func (m *Manager) captureLibraryBaseline(ctx context.Context, s *SQLiteStore, r *LibraryRecoveryRecord) error {
	if m.indexObserver == nil {
		return NewRetryLaterError(errors.New("index_unconfirmed: baseline observer not configured"), 5*time.Minute)
	}
	expected := r.Expected
	expected.Path = ""
	expected.PublishedAt = time.Time{}
	result, err := m.indexObserver.VerifyPublication(ctx, expected)
	if err != nil {
		return NewRetryLaterError(fmt.Errorf("index_unconfirmed: %w", err), 5*time.Minute)
	}
	// 已捕获的映射是保护基线；每次放行重新观察，不吸收外部身份变化。
	if r.BeforeCaptured && result.State == "verified" {
		for _, before := range r.Expected.Protected {
			matched := false
			for _, after := range result.Mappings {
				if before == after {
					matched = true
					break
				}
			}
			if !matched {
				result.State, result.Reason = "external_index_conflict", "protected_mapping_changed"
				break
			}
		}
	}
	if result.State == "external_index_conflict" {
		r.State = "external_index_conflict"
		r.Active = r.Kind == "files"
		r.Reason = result.Reason
		if err := freezeRecoverySeason(r); err != nil {
			return err
		}
		_ = s.saveLibraryRecovery(ctx, r)
		return errors.New("external_index_conflict: " + result.Reason)
	}
	if result.State != "verified" {
		return NewRetryLaterError(errors.New("index_unconfirmed: "+result.Reason), 5*time.Minute)
	}
	if r.Kind == "task" {
		if err := subtitle.ValidateLibraryRecoveryCapability(r.Expected.LibraryRoot, result.Platform); err != nil {
			return NewRetryLaterError(err, 5*time.Minute)
		}
	}
	if r.BeforeCaptured {
		return nil
	}
	r.Expected.Protected = result.Mappings
	r.BeforeCaptured = true
	return s.saveLibraryRecovery(ctx, r)
}
func (m *Manager) confirmLibraryWriterContract(ctx context.Context, r *LibraryRecoveryRecord) error {
	if m.indexObserver == nil || !r.BeforeCaptured {
		return errors.New("index_unconfirmed: writer contract observer missing")
	}
	expected := r.Expected
	expected.Path = ""
	expected.PublishedAt = time.Time{}
	result, err := m.indexObserver.VerifyPublication(ctx, expected)
	if err != nil || result.State != "verified" {
		return fmt.Errorf("capability_unavailable: current writer contract: %s %v", result.Reason, err)
	}
	return subtitle.ValidateLibraryRecoveryCapability(r.Expected.LibraryRoot, result.Platform)
}
func (m *Manager) refreshLibraryIndex(ctx context.Context, s *SQLiteStore) {
	if m.indexObserver == nil {
		return
	}
	records, err := s.libraryRecoveries(ctx)
	if err != nil {
		return
	}
	// 每轮至多观察一个 operation；等待、缺文件和错误也推进游标，避免阻塞其它季。
	m.mu.Lock()
	var selected *LibraryRecoveryRecord
	for _, r := range records {
		if r.InvalidJournal || r.Kind == "control" {
			continue
		}
		if r.State != "index_unconfirmed" && r.State != "published" {
			continue
		}
		if r.Expected.Scope == "append_by_admission" && m.recoveryNow().Before(r.Expected.ObserveNotBefore) {
			continue
		}
		if selected == nil {
			selected = r
		}
		if r.OperationID > m.libraryIndexCursor {
			selected = r
			break
		}
	}
	if selected != nil {
		m.libraryIndexCursor = selected.OperationID
	}
	m.mu.Unlock()
	if selected == nil {
		return
	}
	r := selected
	if r.Expected.Scope == "append_by_admission" {
		_ = m.observeAppendPublication(ctx, LibraryPublicationRequest{LibraryRoot: r.Expected.LibraryRoot, Path: r.Expected.Path, TaskID: r.TaskID, SessionID: r.Expected.AssignmentSession, Phase: "published"}, records)
		return
	}

	if r.State != "index_unconfirmed" && r.State != "published" {
		return
	}
	before, err := subtitle.CaptureLibraryFileState(r.Expected.Path, false)
	if err != nil {
		return
	}
	revision := fmt.Sprintf("%d:%d:%d:%d", before.Device, before.Inode, before.Size, before.MtimeNS)
	if revision != r.Expected.MediaRevision {
		r.State = "external_index_conflict"
		r.Active = r.Kind == "files"
		r.Reason = "published_media_revision_changed"
		_ = freezeRecoverySeason(r)
		_ = s.saveLibraryRecovery(ctx, r)
		return
	}
	if !r.BeforeCaptured {
		return
	}
	observation, err := m.indexObserver.VerifyPublication(ctx, r.Expected)
	if err != nil {
		return
	}
	after, err := subtitle.CaptureLibraryFileState(r.Expected.Path, false)
	if err != nil || !equalJSON(before, after) {
		observation.State = "external_index_conflict"
		observation.Reason = "media_changed_during_observation"
	}
	if observation.State != "verified" && observation.State != "external_index_conflict" {
		return
	}
	r.State = observation.State
	r.Observation = observation
	r.Reason = observation.Reason
	r.Active = r.State == "external_index_conflict" && r.Kind == "files"
	if r.State == "external_index_conflict" {
		_ = freezeRecoverySeason(r)
	}
	_ = s.saveLibraryRecovery(ctx, r)
}

func completeRecoveryPublication(path string) error {
	if err := subtitle.ValidatePublishedLibraryEpisode(path); err != nil {
		return err
	}
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	meta, err := subtitle.LoadMetadata(stem + ".subtitle.json")
	if err != nil {
		return err
	}
	if meta.Status != subtitle.StatusCompleted || meta.OutputPath != path || len(meta.Segments) == 0 || meta.SRTPath != stem+".srt" || meta.ASSPath != stem+".ass" || meta.RecordMeta["live_session_media_role"] != "aggregate" {
		return errors.New("unconfirmed aggregate checkpoint")
	}
	for _, p := range []string{meta.OutputPath, meta.SRTPath, meta.ASSPath} {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return errors.New("aggregate checkpoint unavailable")
		}
	}
	return nil
}

func libraryEvidenceFingerprint(season string) string {
	entries, err := os.ReadDir(season)
	if err != nil {
		return subtitle.LibraryRecoveryDigest([]byte(err.Error()))
	}
	var parts []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(season, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			parts = append(parts, path+err.Error())
			continue
		}
		value := fmt.Sprintf("%s:%d:%d:%d", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if strings.HasSuffix(path, ".nfo") || strings.HasSuffix(path, ".subtitle.json") {
			if b, e := os.ReadFile(path); e == nil {
				value += subtitle.LibraryRecoveryDigest(b)
			}
		}
		parts = append(parts, value)
	}
	return subtitle.LibraryRecoveryDigest([]byte(strings.Join(parts, "\n")))
}

func validateRecoverySources(r *LibraryRecoveryRecord) error {
	for _, before := range r.ProtectedSources {
		after, err := subtitle.CaptureLibraryFileState(before.Path, before.SHA != "")
		if err != nil {
			return err
		}
		if !equalJSON(before, after) {
			return errors.New("protected_source_changed: " + before.Path)
		}
	}
	for _, path := range r.PublishedMissingSources {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return errors.New("published_source_reappeared_or_unreadable: " + path)
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil || parent != filepath.Clean(filepath.Dir(path)) {
			return errors.New("published_source_parent_changed: " + path)
		}
	}
	return nil
}

// 同场前一任务可能正常更新所有分段的知识状态，等其结束后才冻结下一份证明。
func (m *Manager) publishedRecoveryTurn(taskID int64, session RecordingSession, records map[int64]*LibraryRecoveryRecord) error {
	for id := range session.Tasks {
		otherID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return err
		}
		if otherID == taskID {
			continue
		}
		other, err := m.store.GetTask(m.ctx, otherID)
		if err != nil {
			return err
		}
		recovery := records[otherID]
		if other.Status == PipelineStatusPending || other.Status == PipelineStatusRunning || (other.Status == PipelineStatusFailed && recovery != nil && len(recovery.PublishedMissingSources) > 0) {
			return errors.New("waiting_for_same_session_recovery")
		}
	}
	return nil
}

// protectPublishedMissingSource 固定缺源事实及其发布证明；后续每次续跑仍核对这些字节。
func (m *Manager) protectPublishedMissingSource(r *LibraryRecoveryRecord, sessionID string, source SessionMediaSource) error {
	checkpoint, err := subtitle.CaptureLibraryFileState(source.MetadataPath, true)
	if err != nil {
		return err
	}
	meta, err := subtitle.LoadMetadata(source.MetadataPath)
	if err != nil {
		return err
	}
	owner, err := m.store.GetTask(m.ctx, source.TaskID)
	if err != nil {
		return err
	}
	if owner.RecordInfo.LiveSessionID != sessionID || owner.RecordInfo.RecordingProducerID != source.ProducerID || owner.Status == PipelineStatusCancelled {
		return errors.New("published_source_owner_mismatch")
	}
	workStem := strings.TrimSuffix(source.LibraryPath, ".mp4")
	if meta.Status != subtitle.StatusCompleted || len(meta.Segments) == 0 || meta.SourcePath != source.InputPath || meta.OutputPath != source.LibraryPath || meta.SRTPath != workStem+".srt" || meta.ASSPath != workStem+".ass" || meta.RecordMeta["live_session_id"] != sessionID || meta.RecordMeta["recording_producer_id"] != source.ProducerID || meta.RecordMeta["pipeline_task_id"] != strconv.FormatInt(source.TaskID, 10) {
		return errors.New("published_source_checkpoint_mismatch")
	}
	stem := strings.TrimSuffix(meta.SourcePublicationPath, ".mp4")
	proof := []subtitle.LibraryFileState{checkpoint}
	for _, path := range []string{meta.SourcePublicationPath, stem + ".subtitle.json", stem + ".nfo", stem + ".srt", stem + ".ass"} {
		state, err := subtitle.CaptureLibraryFileState(path, path != meta.SourcePublicationPath)
		if err != nil {
			return err
		}
		proof = append(proof, state)
	}
	if err := subtitle.ValidateRecordingSourcePublication(r.Expected.LibraryRoot, meta.SourcePublicationPath, source.InputPath, sessionID, source.ProducerID); err != nil {
		return fmt.Errorf("missing_source_publication_unconfirmed: %s: %w", source.InputPath, err)
	}
	r.ProtectedSources = append(r.ProtectedSources, proof...)
	r.PublishedMissingSources = append(r.PublishedMissingSources, source.InputPath)
	return validateRecoverySources(r)
}

func (m *Manager) taskLibrarySeason(task *PipelineTask, root string) string {
	if rs, err := m.recordingStore(); err == nil && task.RecordInfo.LiveSessionID != "" {
		if session, e := rs.RecordingSession(m.ctx, task.RecordInfo.LiveSessionID); e == nil {
			for _, source := range session.Sources() {
				if source.TaskID == task.ID {
					return subtitle.LibrarySeasonForInput(root, source.LibraryPath, task.RecordInfo.HostName, task.RecordInfo.StartTime)
				}
			}
		}
	}
	if len(task.CurrentFiles) > 0 {
		return subtitle.LibrarySeasonForInput(root, task.CurrentFiles[0].Path, task.RecordInfo.HostName, task.RecordInfo.StartTime)
	}
	return subtitle.LibrarySeasonPath(root, task.RecordInfo.HostName)
}
func (m *Manager) sameLibraryTaskSeason(a, b *PipelineTask) bool {
	cfg := configs.GetCurrentConfig()
	if cfg == nil {
		return a.RecordInfo.HostName == b.RecordInfo.HostName
	}
	root := cfg.Subtitle.GetEffectiveLibraryRoot(cfg.OutPutPath)
	return m.taskLibrarySeason(a, root) == m.taskLibrarySeason(b, root)
}

func freezeRecoverySeason(r *LibraryRecoveryRecord) error {
	marker := subtitle.LibraryRecoveryMarker(filepath.Join(r.Season, ".recovery"))
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(r.OperationID)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return err
	}
	return subtitle.SyncLibraryRecoveryDirectory(filepath.Dir(marker))
}

// libraryAppendCommit仅接C的短提交边界。真正共同锁/CAS由B实现该接口。
func (m *Manager) libraryAppendCommit(ctx context.Context, req LibraryPublicationRequest) error {
	adapter, ok := any(m).(LibraryAppendCommitter)
	if !ok {
		return errors.New("append_commit_guard_unavailable")
	}
	if req.Phase == "append_capability" {
		if _, ok := any(m).(LibraryAggregateComputeController); !ok {
			return errors.New("aggregate_compute_guard_unavailable")
		}
		if _, ok := any(m).(interface {
			captureLibraryAppendSnapshot(context.Context, LibraryPublicationRequest) error
		}); !ok {
			return errors.New("append_snapshot_guard_unavailable")
		}
		return nil
	}
	expected := req.AppendCommit
	if expected == nil || req.TaskID <= 0 || req.SessionID == "" || expected.SeasonPath != filepath.Dir(req.Path) || expected.TargetPath != req.Path || expected.OperationID == "" || expected.AssignmentRevision <= 0 {
		return errors.New("append commit owner/revision missing")
	}
	if req.Phase == "append_compute_permission" || req.Phase == "append_compute_required" {
		controller, ok := any(m).(LibraryAggregateComputeController)
		if !ok {
			if req.Phase == "append_compute_permission" && req.ComputeAllowed != nil {
				*req.ComputeAllowed = false
				return nil
			}
			return errors.New("aggregate_compute_guard_unavailable")
		}
		if req.Phase == "append_compute_permission" {
			if req.ComputeAllowed == nil {
				return errors.New("compute permission output required")
			}
			allowed, err := controller.AllowLibraryAggregateCompute(ctx, expected)
			*req.ComputeAllowed = allowed
			return err
		}
		if req.ComputeDue == nil {
			return errors.New("compute due output required")
		}
		due, err := controller.RequireLibraryAggregateCompute(ctx, expected)
		*req.ComputeDue = due
		return err
	}
	if req.Phase == "append_commit" {
		if expected.ExpectedTask == nil || expected.ExpectedTask.ID != req.TaskID || expected.ExpectedSession.ID != req.SessionID || len(expected.ExpectedMembers) == 0 || req.CommitFiles == nil {
			return errors.New("append commit snapshot incomplete")
		}
		if err := m.verifyAppendIndexBaseline(ctx, req); err != nil {
			return err
		}
		return adapter.WithLibraryAppendCommit(ctx, expected, req.CommitFiles)
	}
	capturer, ok := any(m).(interface {
		captureLibraryAppendSnapshot(context.Context, LibraryPublicationRequest) error
	})
	if !ok {
		return errors.New("append_snapshot_guard_unavailable")
	}
	if err := capturer.captureLibraryAppendSnapshot(ctx, req); err != nil {
		return err
	}
	task := expected.ExpectedTask
	if task == nil || task.Status != PipelineStatusRunning || task.StartedAt == nil || task.CurrentStage < 0 || task.PipelineConfig == nil || task.CurrentStage >= len(task.PipelineConfig.Stages) || task.PipelineConfig.Stages[task.CurrentStage].Name != StageNameSubtitleGenerate {
		return errors.New("append requires current subtitle claim")
	}
	if task.NotBefore != nil && task.NotBefore.After(time.Now()) {
		return errors.New("append_explicit_hold")
	}
	for _, member := range expected.ExpectedMembers {
		if member.Status == PipelineStatusCancelled || member.NotBefore != nil && member.NotBefore.After(time.Now()) {
			return errors.New("append member cancelled or explicit hold")
		}
	}
	return m.verifyAppendIndexBaseline(ctx, req)
}

// LibraryAppendIndexConflictError由持season锁的调用者冻结该season，避免反向取锁。
type LibraryAppendIndexConflictError struct{ Reason string }

func (e *LibraryAppendIndexConflictError) Error() string {
	return "external_index_conflict: " + e.Reason
}

// 新target尚未公开，只核原已知mapping及ledger内明确owned pending；不把索引延迟当全库成功。
func (m *Manager) verifyAppendIndexBaseline(ctx context.Context, req LibraryPublicationRequest) error {
	if m.indexObserver == nil {
		return &RetryLaterError{Err: errors.New("append_index_observer_unavailable")}
	}
	observation, err := m.indexObserver.VerifyPublication(ctx, LibraryIndexExpected{Scope: "append_by_admission", LibraryRoot: req.LibraryRoot, Season: filepath.Dir(req.Path), Show: filepath.Base(filepath.Dir(filepath.Dir(req.Path)))})
	if err != nil {
		return err
	}
	if observation.State == "external_index_conflict" {
		return &LibraryAppendIndexConflictError{Reason: observation.Reason}
	}
	if observation.State != "verified" {
		return &RetryLaterError{Err: errors.New("append_index_baseline_unconfirmed: " + observation.Reason)}
	}
	if req.IndexObservation != nil {
		*req.IndexObservation = observation
	}
	return nil
}

func appendIndexDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 15 * time.Minute
	case 4:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}

// observeAppendPublication只读索引；媒体公开状态由ledger决定，不重发媒体。
func (m *Manager) observeAppendPublication(ctx context.Context, req LibraryPublicationRequest, records []*LibraryRecoveryRecord) error {
	season := filepath.Dir(req.Path)
	ledger, err := subtitle.LoadLibraryAssignments(season)
	if err != nil {
		return err
	}
	var record *LibraryRecoveryRecord
	for _, r := range records {
		if r.Kind == "task" && r.TaskID == req.TaskID {
			record = r
			break
		}
	}
	if record == nil {
		record = &LibraryRecoveryRecord{OperationID: fmt.Sprintf("task-%d", req.TaskID), Kind: "task", TaskID: req.TaskID, Season: season}
	}
	stat, err := subtitle.AssignmentStat(req.Path)
	if err != nil {
		return err
	}
	ordinal, err := subtitle.PublishedLibraryOrdinal(req.Path)
	if err != nil {
		return err
	}
	expected := record.Expected
	expected.Scope = "append_by_admission"
	expected.LibraryRoot = req.LibraryRoot
	expected.Season = season
	expected.Show = filepath.Base(filepath.Dir(season))
	expected.Path = req.Path
	expected.Ordinal = ordinal
	expected.Identity = libraryPathIdentity(req.Path)
	expected.MediaSize = stat.Size
	expected.AssignmentSession = req.SessionID
	expected.AssignmentRevision = ledger.Revision
	found := false
	for _, a := range ledger.Assignments {
		if a.TargetPath == req.Path && a.State == "published" {
			expected.AssignmentOperation = a.OperationID
			if a.PublishedAt != nil {
				expected.PublishedAt = *a.PublishedAt
			}
			found = true
		}
	}
	for _, e := range ledger.LegacyEntries {
		if e.Path == req.Path {
			found = true
		}
	}
	if !found {
		return errors.New("published path has no ledger owner")
	}
	record.Expected = expected
	observation := LibraryIndexObservation{State: "index_unconfirmed", Reason: "observer_not_configured"}
	if m.indexObserver != nil {
		observation, err = m.indexObserver.VerifyPublication(ctx, expected)
		if err != nil {
			observation = LibraryIndexObservation{State: "index_unconfirmed", Reason: err.Error()}
		}
	}
	after, err := subtitle.AssignmentStat(req.Path)
	if err != nil || after != stat {
		observation.State = "external_index_conflict"
		observation.Reason = "publication_media_changed_during_observation"
	}
	if observation.State != "target_verified" && observation.State != "external_index_conflict" {
		observation.State = "index_unconfirmed"
	}
	record.State = observation.State
	record.Observation = observation
	record.Reason = observation.Reason
	record.Expected.ObserveAttempts++
	record.Expected.ObserveNotBefore = m.recoveryNow().Add(appendIndexDelay(record.Expected.ObserveAttempts))
	if observation.State == "target_verified" {
		record.Expected.ObserveNotBefore = time.Time{}
	}
	lease, err := subtitle.LockLibraryAssignments(ctx, season)
	if err != nil {
		return err
	}
	if observation.State == "external_index_conflict" {
		err = lease.Block(observation.Reason)
	} else {
		var mappings []subtitle.AssignmentIndexMapping
		for _, mapping := range observation.Mappings {
			mappings = append(mappings, assignmentMappingFromIndex(mapping))
		}
		err = lease.MergeIndex(mappings)
	}
	lease.Close()
	if err != nil {
		return err
	}
	if err = sMustSave(m, ctx, record); err != nil {
		return err
	}
	// 索引等待或真实冲突不反向把已经耐久公开的task媒体结果改成失败。
	return nil
}

// LibrarySessionIncompleteError保留真实同场缺口，不把旧的其他session混入阻塞成员。
type LibrarySessionIncompleteError struct {
	Reason            string
	BlockingMemberIDs []int64
}

func (e *LibrarySessionIncompleteError) Error() string {
	return fmt.Sprintf("%s blocking_members=%v", e.Reason, e.BlockingMemberIDs)
}
func NewLibrarySessionIncompleteError(session RecordingSession) error {
	e := &LibrarySessionIncompleteError{Reason: "session_incomplete"}
	if !session.Sealed() {
		e.Reason = "session_unsealed"
	}
	for id, member := range session.Tasks {
		if !member.Ready {
			if parsed, err := strconv.ParseInt(id, 10, 64); err == nil {
				e.BlockingMemberIDs = append(e.BlockingMemberIDs, parsed)
			}
		}
	}
	sort.Slice(e.BlockingMemberIDs, func(i, j int) bool { return e.BlockingMemberIDs[i] < e.BlockingMemberIDs[j] })
	return e
}
