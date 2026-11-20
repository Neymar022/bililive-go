//go:build linux

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLibraryRecoveryCancelledPreparedDoesNotStartWrites(t *testing.T) {
	_, db, req := recoveryLinuxFixture(t)
	before, err := subtitle.PlanLibraryIdentityRecovery(filepath.Dir(req.Path))
	require.NoError(t, err)
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	m := NewManager(context.Background(), s, nil, nil)
	m.SetLibraryIndexObserver(fixtureIndexObserver{})
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(p string) error {
		if p == "prepared" {
			return errors.New("simulate interruption before first rename")
		}
		return nil
	}})
	require.Error(t, m.EnsureLibraryPublication(context.Background(), req))
	require.NoError(t, m.CancelTask(req.TaskID))
	require.NoError(t, s.Close())
	s, err = NewSQLiteStore(db)
	require.NoError(t, err)
	defer s.Close()
	next := NewManager(context.Background(), s, nil, nil)
	next.SetLibraryIndexObserver(fixtureIndexObserver{})
	require.NoError(t, next.recoverLibraryJournals())
	task, err := s.GetTask(context.Background(), req.TaskID)
	require.NoError(t, err)
	changed := 0
	for _, mem := range before.Members {
		data, e := os.ReadFile(mem.NFO.Path)
		require.NoError(t, e)
		if string(data) != string(mem.Before) {
			changed++
		}
	}
	t.Logf("cancelled=%v changed_NFO=%d", task.Status, changed)
	require.Equal(t, 0, changed, "prepared/all-before cancelled operation must not initiate NFO writes")
}
func TestLibraryRecoveryTerminalJournalReconcilesMarker(t *testing.T) {
	_, db, req := recoveryLinuxFixture(t)
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	m := NewManager(context.Background(), s, nil, nil)
	m.SetLibraryIndexObserver(fixtureIndexObserver{})
	require.NoError(t, m.EnsureLibraryPublication(context.Background(), req))
	marker := subtitle.LibraryRecoveryMarker(req.Path)
	markerData, err := os.ReadFile(marker)
	require.NoError(t, err)
	nfo, err := subtitle.BuildLibraryEpisodeNFO(req.Path, req.RecordedAt, "")
	require.NoError(t, err)
	stem := req.Path[:len(req.Path)-4]
	require.NoError(t, os.WriteFile(req.Path, []byte("new-complete-media"), 0644))
	require.NoError(t, os.WriteFile(stem+".nfo", []byte(nfo), 0644))
	for _, suffix := range []string{".srt", ".ass"} {
		require.NoError(t, os.WriteFile(stem+suffix, []byte("subtitles"), 0644))
	}
	require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", subtitle.Metadata{Status: subtitle.StatusCompleted, OutputPath: req.Path, SRTPath: stem + ".srt", ASSPath: stem + ".ass", Segments: []subtitle.Segment{{Text: "字幕"}}, RecordMeta: map[string]any{"live_session_media_role": "aggregate"}}))
	records, err := s.libraryRecoveries(context.Background())
	require.NoError(t, err)
	require.NoError(t, m.observeLibraryPublication(context.Background(), req, records))
	records, err = s.libraryRecoveries(context.Background())
	require.NoError(t, err)
	require.Equal(t, "index_unconfirmed", records[0].State)
	require.False(t, records[0].Active)
	require.NoError(t, completeRecoveryPublication(req.Path))
	// 还原 442 行已提交、447 行 remove 尚未执行时的确切marker，成品和journal来自正常路径。
	require.NoError(t, os.WriteFile(marker, markerData, 0600))
	require.NoError(t, s.Close())
	s, err = NewSQLiteStore(db)
	require.NoError(t, err)
	defer s.Close()
	next := NewManager(context.Background(), s, nil, nil)
	next.SetLibraryIndexObserver(fixtureIndexObserver{})
	require.NoError(t, next.recoverLibraryJournals())
	err = subtitle.AssertLibraryPublicationReady(req.Path)
	t.Logf("post_restart_writer_gate=%v", err)
	require.NoError(t, err, "terminal journal state must reconcile a leftover recovery marker")
}

// 成品与 journal 均经正式入口提交，在终态提交后真实杀进程。
func TestLibraryRecoveryTerminalCommitSIGKILL(t *testing.T) {
	_, db, req := recoveryLinuxFixture(t)
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	command := exec.Command(os.Args[0], "-test.run", "^TestLibraryRecoveryTerminalCommitChild$")
	command.Env = append(os.Environ(), "RECOVERY_TERMINAL_DB="+db, "RECOVERY_TERMINAL_REQUEST="+string(raw))
	output, err := command.CombinedOutput()
	require.Error(t, err, string(output))
	exit, ok := err.(*exec.ExitError)
	require.True(t, ok)
	require.Equal(t, syscall.SIGKILL, exit.Sys().(syscall.WaitStatus).Signal(), string(output))
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	defer s.Close()
	rows, err := s.libraryRecoveries(context.Background())
	require.NoError(t, err)
	require.Equal(t, "index_unconfirmed", rows[0].State)
	require.Error(t, subtitle.AssertLibraryPublicationReady(req.Path))
	next := NewManager(context.Background(), s, nil, nil)
	require.NoError(t, next.recoverLibraryJournals())
	require.NoError(t, next.recoverLibraryJournals())
	require.NoError(t, subtitle.AssertLibraryPublicationReady(req.Path))
	require.NoError(t, completeRecoveryPublication(req.Path))
}

func TestLibraryRecoveryTerminalCommitChild(t *testing.T) {
	db := os.Getenv("RECOVERY_TERMINAL_DB")
	if db == "" {
		t.Skip("仅由终态SIGKILL父测试启动")
	}
	var req LibraryPublicationRequest
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("RECOVERY_TERMINAL_REQUEST")), &req))
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	m := NewManager(context.Background(), s, nil, nil)
	m.SetLibraryIndexObserver(fixtureIndexObserver{})
	require.NoError(t, m.EnsureLibraryPublication(context.Background(), req))
	writeRecoveryCompletedPublication(t, req)
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
		if point == "observation_committed" {
			require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGKILL))
		}
		return nil
	}})
	rows, err := s.libraryRecoveries(context.Background())
	require.NoError(t, err)
	require.NoError(t, m.observeLibraryPublication(context.Background(), req, rows))
	t.Fatal("未到达终态提交崩溃边界")
}

func writeRecoveryCompletedPublication(t *testing.T, req LibraryPublicationRequest) {
	t.Helper()
	nfo, err := subtitle.BuildLibraryEpisodeNFO(req.Path, req.RecordedAt, "")
	require.NoError(t, err)
	stem := strings.TrimSuffix(req.Path, ".mp4")
	require.NoError(t, os.WriteFile(req.Path, []byte("new-complete-media"), 0644))
	require.NoError(t, os.WriteFile(stem+".nfo", []byte(nfo), 0644))
	for _, suffix := range []string{".srt", ".ass"} {
		require.NoError(t, os.WriteFile(stem+suffix, []byte("subtitles"), 0644))
	}
	require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", subtitle.Metadata{Status: subtitle.StatusCompleted, OutputPath: req.Path, SRTPath: stem + ".srt", ASSPath: stem + ".ass", Segments: []subtitle.Segment{{Text: "字幕"}}, RecordMeta: map[string]any{"live_session_media_role": "aggregate"}}))
}

func TestLibraryRecoveryTerminalCleanupPreservesUnknownOrChangedEvidence(t *testing.T) {
	for _, damage := range []string{"unknown_marker", "missing_product", "changed_plan_member"} {
		t.Run(damage, func(t *testing.T) {
			_, db, req := recoveryLinuxFixture(t)
			s, err := NewSQLiteStore(db)
			require.NoError(t, err)
			defer s.Close()
			m := NewManager(context.Background(), s, nil, nil)
			m.SetLibraryIndexObserver(fixtureIndexObserver{})
			require.NoError(t, m.EnsureLibraryPublication(context.Background(), req))
			writeRecoveryCompletedPublication(t, req)
			rows, err := s.libraryRecoveries(context.Background())
			require.NoError(t, err)
			marker := subtitle.LibraryRecoveryMarker(req.Path)
			owner, err := os.ReadFile(marker)
			require.NoError(t, err)
			require.NoError(t, m.observeLibraryPublication(context.Background(), req, rows))
			if damage == "unknown_marker" {
				owner = []byte("other-operation")
			}
			require.NoError(t, os.WriteFile(marker, owner, 0600))
			if damage == "missing_product" {
				require.NoError(t, os.Remove(req.Path))
			}
			if damage == "changed_plan_member" {
				require.NoError(t, os.WriteFile(rows[0].Plan.Members[0].NFO.Path, []byte("third value"), 0700))
			}
			require.Error(t, m.recoverLibraryJournals())
			got, err := os.ReadFile(marker)
			require.NoError(t, err)
			require.Equal(t, owner, got)
		})
	}
}

func TestLibraryRecoveryCancellationBeforeFirstRename(t *testing.T) {
	for _, point := range []string{"prepared", "intent_0", "before_rename_0"} {
		t.Run(point, func(t *testing.T) {
			_, db, req := recoveryLinuxFixture(t)
			before, err := subtitle.PlanLibraryIdentityRecovery(filepath.Dir(req.Path))
			require.NoError(t, err)
			s, err := NewSQLiteStore(db)
			require.NoError(t, err)
			defer s.Close()
			m := NewManager(context.Background(), s, nil, nil)
			m.SetLibraryIndexObserver(fixtureIndexObserver{})
			m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(p string) error {
				if p == point {
					return m.CancelTask(req.TaskID)
				}
				return nil
			}})
			require.ErrorIs(t, m.EnsureLibraryPublication(context.Background(), req), context.Canceled)
			for _, member := range before.Members {
				data, err := os.ReadFile(member.NFO.Path)
				require.NoError(t, err)
				require.Equal(t, member.Before, data)
			}
			task, err := s.GetTask(context.Background(), req.TaskID)
			require.NoError(t, err)
			require.Equal(t, PipelineStatusCancelled, task.Status)
		})
	}
}
func TestLibraryRecoveryChangedEvidenceWakesBlockedTask(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	cfg := configs.NewConfig()
	cfg.Subtitle.LibraryRoot = filepath.Join(root, "video")
	require.NoError(t, os.MkdirAll(cfg.Subtitle.LibraryRoot, 0755))
	cfg.Subtitle.Schedule.Enabled = true
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	defer configs.SetCurrentConfig(previous)
	require.NoError(t, store.OpenRecordingSession(ctx, "42", "room"))
	origin, err := store.BeginRecordingProducer(ctx, "room")
	require.NoError(t, err)
	input := filepath.Join(root, "input.mp4")
	require.NoError(t, os.WriteFile(input, []byte("source"), 0644))
	task := NewPipelineTask(RecordInfo{LiveID: "room", HostName: "Host", LiveSessionID: "42", RecordingProducerID: origin.ProducerID, StartTime: time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)}, &PipelineConfig{Stages: []StageConfig{{Name: StageNameSubtitleGenerate}}}, []FileInfo{NewVideoFileInfo(input)})
	task.Status = PipelineStatusFailed
	task.ErrorMessage = "published NFO identity mismatch"
	require.NoError(t, store.CreateTask(ctx, task))
	require.NoError(t, store.UpdateTask(ctx, task))
	require.NoError(t, store.CompleteRecordingTaskMedia(ctx, "42", task.ID, []SessionMediaSource{{TaskID: task.ID, ProducerID: origin.ProducerID, InputPath: input, LibraryPath: filepath.Join(root, "completed.mp4"), MetadataPath: filepath.Join(root, "completed.subtitle.json")}}))
	for _, path := range []string{filepath.Join(root, "completed.mp4"), filepath.Join(root, "completed.srt"), filepath.Join(root, "completed.ass")} {
		require.NoError(t, os.WriteFile(path, []byte("completed"), 0644))
	}
	require.NoError(t, subtitle.SaveMetadata(filepath.Join(root, "completed.subtitle.json"), subtitle.Metadata{Status: subtitle.StatusCompleted, SourcePath: input, OutputPath: filepath.Join(root, "completed.mp4"), SRTPath: filepath.Join(root, "completed.srt"), ASSPath: filepath.Join(root, "completed.ass"), Segments: []subtitle.Segment{{Text: "字幕"}}, RecordMeta: map[string]any{"live_session_id": "42", "recording_producer_id": origin.ProducerID, "pipeline_task_id": fmt.Sprint(task.ID)}}))
	require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	m := NewManager(ctx, store, nil, nil)
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Now: func() time.Time { return now }})

	season := m.taskLibrarySeason(task, cfg.Subtitle.LibraryRoot)
	require.NoError(t, os.MkdirAll(season, 0755))
	evidence := filepath.Join(season, "evidence.nfo")
	require.NoError(t, os.WriteFile(evidence, []byte("old"), 0644))
	old := libraryEvidenceFingerprint(season)
	r := &LibraryRecoveryRecord{OperationID: fmt.Sprintf("task-%d", task.ID), Kind: "task", TaskID: task.ID, Season: season, State: "blocked", Fingerprint: old, NextAttempt: now.Add(-time.Hour)}
	require.NoError(t, store.saveLibraryRecovery(ctx, r))
	blocked := &LibraryRecoveryRecord{OperationID: "blocked-files", Kind: "files", TaskID: task.ID, Season: season, State: "blocked", Active: true, Fingerprint: old, Reason: "identity_evidence_missing"}
	require.NoError(t, store.saveLibraryRecovery(ctx, blocked))
	for i := 0; i < 3; i++ {
		m.compensateLibraryFailures()
	}
	unchanged, e := store.libraryRecoveries(ctx)
	require.NoError(t, e)
	for _, row := range unchanged {
		require.Equal(t, int64(1), row.Revision, "同证据硬拒绝无journal写入")
	}
	hold := now.Add(72 * time.Hour).UTC()
	task.NotBefore = &hold
	require.NoError(t, store.UpdateTask(ctx, task))
	require.NoError(t, os.WriteFile(evidence, []byte("new valid source evidence"), 0644))
	for i := 0; i < 4; i++ {
		m.compensateLibraryFailures()
	}
	rows, err := store.libraryRecoveries(ctx)
	require.NoError(t, err)
	for _, got := range rows {
		if got.Kind == "task" {
			t.Logf("task recovery after changed evidence: state=%s fingerprint_updated=%v attempts=%d", got.State, got.Fingerprint != old, got.Attempts)
			require.Equal(t, "waiting", got.State)
			require.Equal(t, 0, got.Attempts)
			require.False(t, got.NextAttempt.Before(hold), "证据更新不缩短原hold")
			require.Equal(t, int64(2), got.Revision, "新证据只安排一次fresh分类")
		}
	}
}
func TestLibraryRecoveryCapabilityRejectsUnprobedBuild(t *testing.T) {
	library, _, _ := recoveryLinuxFixture(t)
	path := filepath.Join(subtitle.LibraryRecoveryRoot(library), "capability.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var cap map[string]any
	require.NoError(t, json.Unmarshal(raw, &cap))
	cap["platform_build_sha256"] = strings.Repeat("c", 64)
	raw, err = json.Marshal(cap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	observed, err := (fixtureIndexObserver{}).VerifyPublication(context.Background(), LibraryIndexExpected{})
	require.NoError(t, err)
	err = subtitle.ValidateLibraryRecoveryCapability(library, observed.Platform)
	t.Logf("unprobed build capability validation=%v", err)
	require.Error(t, err, "能力必须匹配当前运行构建SHA，不能只检查非空字段")
}

type platformFixtureObserver struct {
	change func(*subtitle.LibraryRecoveryPlatform)
}

func (o platformFixtureObserver) VerifyPublication(ctx context.Context, e LibraryIndexExpected) (LibraryIndexObservation, error) {
	result, err := (fixtureIndexObserver{}).VerifyPublication(ctx, e)
	if o.change != nil {
		o.change(&result.Platform)
	}
	return result, err
}

func TestLibraryRecoveryRuntimeBuildMustMatchCapability(t *testing.T) {
	for _, mode := range []string{"same_build_restart", "changed_build", "missing_platform", "stale_platform", "wrong_path", "bad_sha", "bad_time", "v1_capability", "bad_probe_sha", "bad_installed_at", "changed_before_rename"} {
		t.Run(mode, func(t *testing.T) {
			library, db, req := recoveryLinuxFixture(t)
			before, err := subtitle.PlanLibraryIdentityRecovery(filepath.Dir(req.Path))
			require.NoError(t, err)
			s, err := NewSQLiteStore(db)
			require.NoError(t, err)
			defer s.Close()
			m := NewManager(context.Background(), s, nil, nil)
			change := func(p *subtitle.LibraryRecoveryPlatform) {
				switch mode {
				case "same_build_restart":
					p.PID = 999
					p.StartTicks = 999
					p.StartedAt = time.Now().Add(-time.Minute)
				case "changed_build":
					p.BuildSHA256 = strings.Repeat("c", 64)
				case "missing_platform":
					*p = subtitle.LibraryRecoveryPlatform{}
				case "stale_platform":
					p.ObservedAt = time.Now().Add(-2 * time.Minute)
				case "wrong_path":
					p.BuildPath = "/tmp/video_serv"
				case "bad_sha":
					p.BuildSHA256 = strings.Repeat("z", 64)
				case "bad_time":
					p.StartedAt = time.Now().Add(time.Hour)
				}
			}
			m.SetLibraryIndexObserver(platformFixtureObserver{change: change})
			capPath := filepath.Join(subtitle.LibraryRecoveryRoot(library), "capability.json")
			raw, err := os.ReadFile(capPath)
			require.NoError(t, err)
			var capability map[string]any
			require.NoError(t, json.Unmarshal(raw, &capability))
			switch mode {
			case "v1_capability":
				capability["schema_version"] = 1
			case "bad_probe_sha":
				capability["probe_evidence_sha256"] = strings.Repeat("z", 64)
			case "bad_installed_at":
				capability["installed_at"] = "yesterday"
			}
			raw, err = json.Marshal(capability)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(capPath, raw, 0600))
			if mode == "changed_before_rename" {
				m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
					if point == "intent_0" {
						m.SetLibraryIndexObserver(platformFixtureObserver{change: func(p *subtitle.LibraryRecoveryPlatform) { p.BuildSHA256 = strings.Repeat("c", 64) }})
					}
					return nil
				}})
			}
			err = m.EnsureLibraryPublication(context.Background(), req)
			if mode == "same_build_restart" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			for _, member := range before.Members {
				got, e := os.ReadFile(member.NFO.Path)
				require.NoError(t, e)
				if mode == "same_build_restart" {
					require.Equal(t, member.After, got)
				} else {
					require.Equal(t, member.Before, got)
				}
			}
		})
	}
}

func TestLibraryRecoveryBlockedPlanReclassificationIsNarrow(t *testing.T) {
	for _, mode := range []string{"all_before", "partial_after", "third_value"} {
		t.Run(mode, func(t *testing.T) {
			_, db, req := recoveryLinuxFixture(t)
			s, err := NewSQLiteStore(db)
			require.NoError(t, err)
			defer s.Close()
			m := NewManager(context.Background(), s, nil, nil)
			m.SetLibraryIndexObserver(fixtureIndexObserver{})
			boundary := "prepared"
			if mode == "partial_after" {
				boundary = "rename_0"
			}
			m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
				if point == boundary {
					return errors.New("interrupted")
				}
				return nil
			}})
			require.Error(t, m.EnsureLibraryPublication(context.Background(), req))
			m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{})
			rows, err := s.libraryRecoveries(context.Background())
			require.NoError(t, err)
			original := rows[0]
			if mode == "third_value" {
				require.NoError(t, os.WriteFile(original.Plan.Members[0].NFO.Path, []byte("external-third-value"), 0700))
			}
			foreign := filepath.Join(filepath.Dir(req.Path), "unexpected.mkv")
			require.NoError(t, os.WriteFile(foreign, []byte("foreign"), 0644))
			require.Error(t, m.EnsureLibraryPublication(context.Background(), req))
			rows, err = s.libraryRecoveries(context.Background())
			require.NoError(t, err)
			rejectedRevision := rows[0].Revision
			for i := 0; i < 3; i++ {
				require.Error(t, m.EnsureLibraryPublication(context.Background(), req))
			}
			unchanged, err := s.libraryRecoveries(context.Background())
			require.NoError(t, err)
			require.Equal(t, rejectedRevision, unchanged[0].Revision, "同证据硬拒绝不反复写journal")
			require.NoError(t, os.Remove(foreign))
			oldBytes := map[string][]byte{}
			for _, member := range original.Plan.Members {
				oldBytes[member.NFO.Path], err = os.ReadFile(member.NFO.Path)
				require.NoError(t, err)
			}
			err = m.EnsureLibraryPublication(context.Background(), req)
			if mode == "all_before" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			for _, member := range original.Plan.Members {
				got, e := os.ReadFile(member.NFO.Path)
				require.NoError(t, e)
				if mode == "all_before" {
					require.Equal(t, member.After, got)
				} else {
					require.Equal(t, oldBytes[member.NFO.Path], got)
				}
			}
			after, err := s.libraryRecoveries(context.Background())
			require.NoError(t, err)
			if mode == "all_before" {
				require.Len(t, after, 2)
				for _, row := range after {
					if row.OperationID == original.OperationID {
						require.False(t, row.Active)
						require.Equal(t, original.PlanHash, row.PlanHash)
					} else {
						require.True(t, row.Active)
						require.Equal(t, "nfo_verified", row.State)
					}
				}
			} else {
				require.Len(t, after, 1)
				require.Equal(t, "blocked", after[0].State)
			}
		})
	}
}

func TestLibraryRecoveryCancelledPartialStopsPublication(t *testing.T) {
	_, db, req := recoveryLinuxFixture(t)
	plan, err := subtitle.PlanLibraryIdentityRecovery(filepath.Dir(req.Path))
	require.NoError(t, err)
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	defer s.Close()
	m := NewManager(context.Background(), s, nil, nil)
	m.SetLibraryIndexObserver(fixtureIndexObserver{})
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
		if point == "rename_0" {
			return m.CancelTask(req.TaskID)
		}
		return nil
	}})
	require.ErrorIs(t, m.EnsureLibraryPublication(context.Background(), req), context.Canceled, "文件一致性收尾后不能继续发布或resume")
	for _, member := range plan.Members {
		got, e := os.ReadFile(member.NFO.Path)
		require.NoError(t, e)
		require.Equal(t, member.After, got)
	}
	rows, err := s.libraryRecoveries(context.Background())
	require.NoError(t, err)
	require.Equal(t, "cancelled", rows[0].State)
	require.False(t, rows[0].Active)
	task, err := s.GetTask(context.Background(), req.TaskID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusCancelled, task.Status)
}

func TestLibraryRecoveryCallbackRejectsUnownedMarker(t *testing.T) {
	for _, mode := range []string{"unknown", "prepared_other", "prepared_symlink"} {
		t.Run(mode, func(t *testing.T) {
			_, db, req := recoveryLinuxFixture(t)
			plan, err := subtitle.PlanLibraryIdentityRecovery(filepath.Dir(req.Path))
			require.NoError(t, err)
			s, err := NewSQLiteStore(db)
			require.NoError(t, err)
			defer s.Close()
			m := NewManager(context.Background(), s, nil, nil)
			m.SetLibraryIndexObserver(fixtureIndexObserver{})
			marker := subtitle.LibraryRecoveryMarker(req.Path)
			if mode != "unknown" {
				m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
					if point == "prepared" {
						return errors.New("interrupted")
					}
					return nil
				}})
				require.Error(t, m.EnsureLibraryPublication(context.Background(), req))
				m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{})
			}
			if mode == "prepared_symlink" {
				owner, err := os.ReadFile(marker)
				require.NoError(t, err)
				target := marker + ".external"
				require.NoError(t, os.WriteFile(target, owner, 0600))
				require.NoError(t, os.Remove(marker))
				require.NoError(t, os.Symlink(target, marker))
			} else {
				require.NoError(t, os.WriteFile(marker, []byte("unowned-marker"), 0600))
			}
			require.Error(t, m.EnsureLibraryPublication(context.Background(), req))
			for _, member := range plan.Members {
				got, err := os.ReadFile(member.NFO.Path)
				require.NoError(t, err)
				require.Equal(t, member.Before, got)
			}
		})
	}
}

func TestLibraryRecoveryFlatPathKeepsLockAndMarkerWithoutParentWrite(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "legacy.mp4")
	require.NoError(t, os.WriteFile(video, []byte("media"), 0644))
	require.NoError(t, subtitle.SaveMetadata(filepath.Join(root, "legacy.subtitle.json"), subtitle.Metadata{OutputPath: video}))
	require.NoError(t, subtitle.SetKeepSource(video, true))
	marker := subtitle.LibraryRecoveryMarker(video)
	require.True(t, strings.HasPrefix(marker, root+string(os.PathSeparator)), "旧平铺路径不能向上猜库根")
	unlock, err := subtitle.LockLibraryPublicationContext(context.Background(), video)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = subtitle.LockLibraryPublicationContext(ctx, video)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	unlock()
	require.NoError(t, os.WriteFile(marker, []byte("unowned-operation"), 0600))
	require.ErrorContains(t, subtitle.SetKeepSource(video, false), "season_recovery_in_progress")
	metadata, err := subtitle.LoadMetadata(filepath.Join(root, "legacy.subtitle.json"))
	require.NoError(t, err)
	require.True(t, metadata.KeepSource)
}
