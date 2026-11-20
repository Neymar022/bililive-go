//go:build linux

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func recoveryLinuxFixture(t *testing.T) (string, string, LibraryPublicationRequest) {
	t.Helper()
	root := t.TempDir()
	library := filepath.Join(root, "video")
	season := filepath.Join(library, "Host", "Season 01")
	require.NoError(t, os.MkdirAll(season, 0755))
	for i := 0; i < 2; i++ {
		start := time.Date(2026, 5, 12+i, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
		id := (start.UnixMicro() - 1577836800000000) * 8
		media := filepath.Join(season, fmt.Sprintf("Host.S01E%d.%s - old.mp4", id, start.Format("2006-01-02")))
		require.NoError(t, os.WriteFile(media, []byte(fmt.Sprintf("media%d", i)), 0644))
		stem := strings.TrimSuffix(media, ".mp4")
		require.NoError(t, os.WriteFile(stem+".nfo", []byte(fmt.Sprintf("<?xml version=\"1.0\"?>\n<episodedetails><season>1</season><!-- keep --><episode>%d</episode><plot>原字节</plot></episodedetails>\n", i+1)), 0700))
		require.NoError(t, unix.Setxattr(stem+".nfo", "user.fixture", []byte("must-preserve"), 0))
		require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", subtitle.Metadata{Status: subtitle.StatusCompleted, OutputPath: media, RecordMeta: map[string]any{"start_time": start.Format(time.RFC3339Nano)}}))
	}
	state, err := subtitle.CaptureLibraryFileState(library, false)
	require.NoError(t, err)
	cap := map[string]any{"schema_version": 2, "canonical_library_root": library, "library_dev": state.Device, "library_inode": state.Inode, "service_uid": os.Geteuid(), "service_gid": os.Getegid(), "mode": "exclusive_application_nfo_writer", "platform_build_path": subtitle.LibraryPlatformBuildPath, "platform_build_sha256": strings.Repeat("b", 64), "installed_at": time.Now().Format(time.RFC3339Nano), "probe_evidence_sha256": strings.Repeat("a", 64), "library_attributes": state}
	require.NoError(t, os.MkdirAll(subtitle.LibraryRecoveryRoot(library), 0700))
	b, err := json.Marshal(cap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(subtitle.LibraryRecoveryRoot(library), "capability.json"), b, 0600))
	db := filepath.Join(root, "pipeline.db")
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	input := filepath.Join(root, "input.mp4")
	require.NoError(t, os.WriteFile(input, []byte("input"), 0644))
	task := NewPipelineTask(RecordInfo{}, &PipelineConfig{}, []FileInfo{NewVideoFileInfo(input)})
	task.Status = PipelineStatusFailed
	require.NoError(t, s.CreateTask(context.Background(), task))
	require.NoError(t, s.Close())
	req := LibraryPublicationRequest{TaskID: task.ID, LibraryRoot: library, Path: filepath.Join(season, "Host.S01E1685894400000000.2026-09-05 - next.mp4"), SessionID: "next", RecordedAt: time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)}
	return library, db, req
}
func TestLibraryRecoveryActualSIGKILLReentry(t *testing.T) {
	for _, boundary := range []string{"detected", "backup_0_before", "backup_0_after", "backup_1_before", "backup_1_after", "prepared", "intent_0", "before_rename_0", "rename_0", "fsync_0", "intent_1", "rename_1", "fsync_1", "nfo_verified"} {
		t.Run(boundary, func(t *testing.T) {
			library, db, req := recoveryLinuxFixture(t)
			before, err := subtitle.PlanLibraryIdentityRecovery(filepath.Dir(req.Path))
			require.NoError(t, err)
			b, err := json.Marshal(req)
			require.NoError(t, err)
			command := exec.Command(os.Args[0], "-test.run", "^TestLibraryRecoveryCrashChild$")
			command.Env = append(os.Environ(), "RECOVERY_CHILD_DB="+db, "RECOVERY_CHILD_REQUEST="+string(b), "RECOVERY_CHILD_BOUNDARY="+boundary)
			output, err := command.CombinedOutput()
			require.Error(t, err, string(output))
			exit, ok := err.(*exec.ExitError)
			require.True(t, ok)
			require.Equal(t, syscall.SIGKILL, exit.Sys().(syscall.WaitStatus).Signal(), string(output))
			s, err := NewSQLiteStore(db)
			require.NoError(t, err)
			m := NewManager(context.Background(), s, &ManagerConfig{PollInterval: time.Hour}, nil)
			m.SetLibraryIndexObserver(fixtureIndexObserver{})
			require.NoError(t, m.Start(context.Background()))
			task, err := m.GetTask(req.TaskID)
			require.NoError(t, err)
			require.NotNil(t, task.LibraryRecovery)
			require.Equal(t, "nfo_verified", task.LibraryRecovery.State, task.LibraryRecovery.Reason)
			require.NoError(t, subtitle.ValidateLibraryIdentityPlan(before))
			for _, member := range before.Members {
				after, err := os.ReadFile(member.NFO.Path)
				require.NoError(t, err)
				require.Equal(t, member.After, after)
				require.NoError(t, subtitle.ValidatePublishedLibraryEpisode(member.Media.Path))
			}
			require.Equal(t, library, task.LibraryRecovery.Expected.LibraryRoot)
			m.Close(context.Background())
		})
	}
}
func TestLibraryRecoveryCrashChild(t *testing.T) {
	db := os.Getenv("RECOVERY_CHILD_DB")
	if db == "" {
		t.Skip("仅由SIGKILL父测试启动")
	}
	var req LibraryPublicationRequest
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("RECOVERY_CHILD_REQUEST")), &req))
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	m := NewManager(context.Background(), s, nil, nil)
	m.SetLibraryIndexObserver(fixtureIndexObserver{})
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
		if point == os.Getenv("RECOVERY_CHILD_BOUNDARY") {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
		return nil
	}})
	unlock, err := subtitle.LockLibraryPublicationContext(context.Background(), req.Path)
	require.NoError(t, err)
	defer unlock()
	require.NoError(t, m.EnsureLibraryPublication(context.Background(), req))
	t.Fatal("指定中断点未执行")
}
func TestLibraryRecoveryRejectsThirdValueAndCorruptBackup(t *testing.T) {
	for _, damage := range []string{"third_value", "corrupt_backup", "added_member", "wrong_identity", "missing_time", "long_ordinal", "short_identity", "symlink_nfo", "duplicate_id", "gap", "duplicate_ordinal", "bad_xml", "wrong_season", "unknown_media", "early_insertion", "staged_attributes"} {
		t.Run(damage, func(t *testing.T) {
			_, db, req := recoveryLinuxFixture(t)
			s, err := NewSQLiteStore(db)
			require.NoError(t, err)
			defer s.Close()
			m := NewManager(context.Background(), s, nil, nil)
			m.SetLibraryIndexObserver(fixtureIndexObserver{})
			files, err := filepath.Glob(filepath.Join(filepath.Dir(req.Path), "*.nfo"))
			require.NoError(t, err)
			original, err := os.ReadFile(files[0])
			require.NoError(t, err)
			switch damage {
			case "duplicate_id":
				identity := libraryPathIdentity(files[0])
				require.NotEmpty(t, identity)
				nfo := fmt.Sprintf(`<episodedetails><season>1</season><episode>1</episode><uniqueid type="bililive-recorded-at">%s</uniqueid><uniqueid type="bililive-recorded-at">%s</uniqueid></episodedetails>`, identity, identity)
				require.NoError(t, os.WriteFile(files[0], []byte(nfo), 0700))
			case "gap":
				require.NoError(t, os.WriteFile(files[1], []byte(`<episodedetails><season>1</season><episode>3</episode></episodedetails>`), 0700))
			case "duplicate_ordinal":
				require.NoError(t, os.WriteFile(files[1], []byte(`<episodedetails><season>1</season><episode>1</episode></episodedetails>`), 0700))
			case "bad_xml":
				require.NoError(t, os.WriteFile(files[0], []byte(`<episodedetails><episode>1`), 0700))
			case "wrong_season":
				require.NoError(t, os.WriteFile(files[0], []byte(`<episodedetails><season>2</season><episode>1</episode></episodedetails>`), 0700))
			case "unknown_media":
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(req.Path), "foreign.S02E1.mp4"), []byte("foreign"), 0644))
			case "early_insertion":
				req.RecordedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			case "wrong_identity":
				require.NoError(t, os.WriteFile(files[0], []byte(`<episodedetails><season>1</season><episode>1</episode><uniqueid type="bililive-recorded-at">123</uniqueid></episodedetails>`), 0700))
			case "long_ordinal":
				require.NoError(t, os.WriteFile(files[0], []byte(`<episodedetails><season>1</season><episode>10000</episode></episodedetails>`), 0700))
			case "missing_time":
				metaPath := strings.TrimSuffix(files[0], ".nfo") + ".subtitle.json"
				meta, err := subtitle.LoadMetadata(metaPath)
				require.NoError(t, err)
				delete(meta.RecordMeta, "start_time")
				require.NoError(t, subtitle.SaveMetadata(metaPath, meta))
			case "short_identity":
				media := strings.TrimSuffix(files[0], ".nfo") + ".mp4"
				require.NoError(t, os.Rename(media, filepath.Join(filepath.Dir(media), "Host.S01E0001.2026-05-12 - old.mp4")))
			case "symlink_nfo":
				require.NoError(t, os.Rename(files[0], files[0]+".original"))
				require.NoError(t, os.Symlink(files[0]+".original", files[0]))
			default:
				m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
					if point == "prepared" {
						return fmt.Errorf("stop")
					}
					return nil
				}})
				require.Error(t, m.EnsureLibraryPublication(context.Background(), req))
				m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{})
				switch damage {
				case "third_value":
					require.NoError(t, os.WriteFile(files[0], []byte("external"), 0700))
				case "added_member":
					require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(req.Path), "unexpected.mkv"), []byte("external"), 0644))
				case "staged_attributes":
					task, err := m.GetTask(req.TaskID)
					require.NoError(t, err)
					member := task.LibraryRecovery.Plan.Members[0]
					staged := filepath.Join(filepath.Dir(req.Path), ".identity-"+task.LibraryRecovery.OperationID+"-0")
					require.NoError(t, os.WriteFile(staged, member.After, 0644))
				case "corrupt_backup":
					task, err := m.GetTask(req.TaskID)
					require.NoError(t, err)
					backup := filepath.Join(subtitle.LibraryRecoveryRoot(req.LibraryRoot), "operations", task.LibraryRecovery.OperationID, "0000.before")
					require.NoError(t, os.WriteFile(backup, []byte("corrupt"), 0700))
				}
			}
			before, err := os.ReadFile(files[0])
			require.NoError(t, err)
			require.Error(t, m.EnsureLibraryPublication(context.Background(), req))
			after, err := os.ReadFile(files[0])
			require.NoError(t, err)
			require.Equal(t, before, after)
			if damage == "corrupt_backup" || damage == "added_member" || damage == "missing_time" || damage == "short_identity" {
				require.Equal(t, original, after)
			}
		})
	}
}

type fixtureIndexObserver struct{}

func (fixtureIndexObserver) VerifyPublication(ctx context.Context, expected LibraryIndexExpected) (LibraryIndexObservation, error) {
	if expected.Path == "" {
		return LibraryIndexObservation{State: "verified", GeneratedAt: time.Now(), AdapterVersion: "isolated-fixture", Platform: subtitle.LibraryRecoveryPlatform{State: "confirmed", PID: 123, UID: 0, StartTicks: 10, StartedAt: time.Now().Add(-time.Hour), ObservedAt: time.Now(), StartupPath: subtitle.LibraryPlatformStartupPath, BuildPath: subtitle.LibraryPlatformBuildPath, BuildSHA256: strings.Repeat("b", 64)}}, nil
	}
	return LibraryIndexObservation{State: "index_unconfirmed", Reason: "fixture has no PG"}, nil
}

func TestLibraryRecoveryCancellationFinishesFilesWithoutRevivingTask(t *testing.T) {
	_, db, req := recoveryLinuxFixture(t)
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	m := NewManager(context.Background(), s, nil, nil)
	m.SetLibraryIndexObserver(fixtureIndexObserver{})
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
		if point == "rename_0" {
			return m.CancelTask(req.TaskID)
		}
		return nil
	}})
	require.ErrorIs(t, m.EnsureLibraryPublication(context.Background(), req), context.Canceled)
	task, err := m.GetTask(req.TaskID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusCancelled, task.Status)
	require.NoError(t, s.Close())
	s, err = NewSQLiteStore(db)
	require.NoError(t, err)
	next := NewManager(context.Background(), s, &ManagerConfig{PollInterval: time.Hour}, nil)
	next.SetLibraryIndexObserver(fixtureIndexObserver{})
	defer next.Close(context.Background())
	require.NoError(t, next.Start(context.Background()))
	task, err = next.GetTask(req.TaskID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusCancelled, task.Status)
	require.Equal(t, "cancelled", task.LibraryRecovery.State)
	_, err = os.Lstat(subtitle.LibraryRecoveryMarker(req.Path))
	require.True(t, os.IsNotExist(err))
	require.Error(t, next.ResumeTask(req.TaskID))
}
func TestLibraryRecoverySecondWriterUsesOSLockAndPersistentGate(t *testing.T) {
	_, db, req := recoveryLinuxFixture(t)
	unlock, err := subtitle.LockLibraryPublicationContext(context.Background(), req.Path)
	require.NoError(t, err)
	command := exec.Command(os.Args[0], "-test.run", "^TestLibraryRecoveryLockChild$")
	command.Env = append(os.Environ(), "RECOVERY_LOCK_CHILD="+req.Path)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	other, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	otherUnlock, err := subtitle.LockLibraryPublicationContext(other, filepath.Join(req.LibraryRoot, "Other", "Season 01", "next.mp4"))
	require.NoError(t, err)
	otherUnlock()
	unlock()
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	defer s.Close()
	m := NewManager(context.Background(), s, nil, nil)
	m.SetLibraryIndexObserver(fixtureIndexObserver{})
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
		if point == "prepared" {
			return fmt.Errorf("interrupted")
		}
		return nil
	}})
	require.Error(t, m.EnsureLibraryPublication(context.Background(), req))
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(req.Path), "*.mp4"))
	require.NoError(t, err)
	require.ErrorContains(t, subtitle.EnsureLibrarySidecars(context.Background(), paths[0], paths[0], "Host", req.RecordedAt, ""), "season_recovery_in_progress")
}
func TestLibraryRecoveryLockChild(t *testing.T) {
	path := os.Getenv("RECOVERY_LOCK_CHILD")
	if path == "" {
		t.Skip("仅由第二进程锁测试启动")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	unlock, err := subtitle.LockLibraryPublicationContext(ctx, path)
	if err == nil {
		unlock()
		t.Fatal("第二进程不应越过同季OS锁")
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
func TestLibraryRecoverySameSeasonReentryDoesNotReplaceNFOAgain(t *testing.T) {
	_, db, req := recoveryLinuxFixture(t)
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	defer s.Close()
	m := NewManager(context.Background(), s, nil, nil)
	m.SetLibraryIndexObserver(fixtureIndexObserver{})
	ready := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 2)
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
		if point == "prepared" {
			close(ready)
			<-release
		}
		return nil
	}})
	go func() {
		unlock, e := subtitle.LockLibraryPublicationContext(context.Background(), req.Path)
		if e == nil {
			defer unlock()
			e = m.EnsureLibraryPublication(context.Background(), req)
		}
		result <- e
	}()
	<-ready
	go func() {
		unlock, e := subtitle.LockLibraryPublicationContext(context.Background(), req.Path)
		if e == nil {
			defer unlock()
			e = m.EnsureLibraryPublication(context.Background(), req)
		}
		result <- e
	}()
	close(release)
	require.NoError(t, <-result)
	require.NoError(t, <-result)
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(req.Path), "*.nfo"))
	require.NoError(t, err)
	first, err := subtitle.CaptureLibraryFileState(paths[0], true)
	require.NoError(t, err)
	require.NoError(t, m.EnsureLibraryPublication(context.Background(), req))
	after, err := subtitle.CaptureLibraryFileState(paths[0], true)
	require.NoError(t, err)
	require.Equal(t, first, after)
}

func TestRecoveryResumeCommitSIGKILLDoesNotResetCheckpoint(t *testing.T) {
	for _, point := range []string{"resume_requested", "resume_committed", "resume_response"} {
		t.Run(point, func(t *testing.T) {
			root := t.TempDir()
			db := filepath.Join(root, "pipeline.db")
			s, err := NewSQLiteStore(db)
			require.NoError(t, err)
			ctx := context.Background()
			require.NoError(t, s.OpenRecordingSession(ctx, "42", "room"))
			origin, err := s.BeginRecordingProducer(ctx, "room")
			require.NoError(t, err)
			input := filepath.Join(root, "input.mp4")
			require.NoError(t, os.WriteFile(input, []byte("original"), 0644))
			task := NewPipelineTask(RecordInfo{LiveID: "room", LiveSessionID: "42", RecordingProducerID: origin.ProducerID, HostName: "Host", StartTime: time.Now()}, &PipelineConfig{Stages: []StageConfig{{Name: StageNameSubtitleGenerate}}}, []FileInfo{NewVideoFileInfo(input)})
			task.Status = PipelineStatusFailed
			require.NoError(t, s.CreateTask(ctx, task))
			task.ErrorMessage = "published NFO identity mismatch"
			require.NoError(t, s.UpdateTask(ctx, task))
			stem := filepath.Join(root, "completed")
			for _, ext := range []string{".mp4", ".srt", ".ass"} {
				require.NoError(t, os.WriteFile(stem+ext, []byte("checkpoint"), 0644))
			}
			require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", subtitle.Metadata{Status: subtitle.StatusCompleted, SourcePath: input, OutputPath: stem + ".mp4", SRTPath: stem + ".srt", ASSPath: stem + ".ass", Segments: []subtitle.Segment{{Text: "字幕"}}, RecordMeta: map[string]any{"live_session_id": "42", "recording_producer_id": origin.ProducerID, "pipeline_task_id": fmt.Sprint(task.ID)}}))
			require.NoError(t, s.CompleteRecordingTaskMedia(ctx, "42", task.ID, []SessionMediaSource{{TaskID: task.ID, ProducerID: origin.ProducerID, InputPath: input, LibraryPath: stem + ".mp4", MetadataPath: stem + ".subtitle.json"}}))
			require.NoError(t, s.EndRecordingSession(ctx, "room", "normal"))
			require.NoError(t, s.FinishRecordingProducer(ctx, origin, ""))
			require.NoError(t, os.MkdirAll(filepath.Join(root, "video"), 0755))
			cfg := configs.NewConfig()
			cfg.Subtitle.LibraryRoot = filepath.Join(root, "video")
			cfg.Subtitle.Schedule.Enabled = false
			previous := configs.GetCurrentConfig()
			configs.SetCurrentConfig(cfg)
			defer configs.SetCurrentConfig(previous)
			m := NewManager(ctx, s, nil, nil)
			m.compensateLibraryFailures()
			require.NoError(t, s.Close())
			child := exec.Command(os.Args[0], "-test.run", "^TestRecoveryResumeCrashChild$")
			child.Env = append(os.Environ(), "RECOVERY_RESUME_DB="+db, "RECOVERY_RESUME_BOUNDARY="+point)
			output, err := child.CombinedOutput()
			require.Error(t, err, string(output))
			require.Contains(t, err.Error(), "killed")
			s, err = NewSQLiteStore(db)
			require.NoError(t, err)
			defer s.Close()
			m = NewManager(ctx, s, nil, nil)
			m.compensateLibraryFailures()
			got, err := m.GetTask(task.ID)
			require.NoError(t, err)
			require.Equal(t, PipelineStatusPending, got.Status)
			require.Equal(t, "resumed", got.LibraryRecovery.State)
			require.Equal(t, task.CurrentStage, got.CurrentStage)
			require.Equal(t, task.CurrentFiles, got.CurrentFiles)
		})
	}
}
func TestRecoveryResumeCrashChild(t *testing.T) {
	db := os.Getenv("RECOVERY_RESUME_DB")
	if db == "" {
		t.Skip("仅供resume SIGKILL父测试")
	}
	cfg := configs.NewConfig()
	cfg.Subtitle.LibraryRoot = filepath.Join(filepath.Dir(db), "video")
	cfg.Subtitle.Schedule.Enabled = false
	configs.SetCurrentConfig(cfg)
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	m := NewManager(context.Background(), s, nil, nil)
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Boundary: func(point string) error {
		if point == os.Getenv("RECOVERY_RESUME_BOUNDARY") {
			return syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
		return nil
	}})
	m.compensateLibraryFailures()
	t.Fatal("resume中断点未执行")
}
