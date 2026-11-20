//go:build linux

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

// 已发布成品覆盖原片、同场失败任务尚未收敛，覆盖成品已发布而同场任务仍待收尾的保全边界。
func publishedRecoveryFixture(t *testing.T, completedSibling ...bool) (*Manager, *SQLiteStore, *PipelineTask, subtitle.Metadata) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	library := filepath.Join(root, "video")
	require.NoError(t, os.MkdirAll(filepath.Join(library, "Host", "Season 01"), 0755))
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled = true
	cfg.Subtitle.LibraryRoot = library
	cfg.Subtitle.Schedule.Enabled = false
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(previous) })
	store, err := NewSQLiteStore(filepath.Join(root, "pipeline.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	require.NoError(t, store.OpenRecordingSession(ctx, "42", "room"))
	origin, err := store.BeginRecordingProducer(ctx, "room")
	require.NoError(t, err)
	input, err := subtitle.RecordingCapturePath(library, "42", origin.ProducerID, "source.mp4")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(input, []byte("原片"), 0644))
	at := time.Date(2026, 9, 5, 2, 0, 0, 0, time.UTC)
	task := NewPipelineTask(RecordInfo{LiveID: "room", HostName: "Host", LiveSessionID: "42", RecordingProducerID: origin.ProducerID, StartTime: at}, &PipelineConfig{Stages: []StageConfig{{Name: StageNameSubtitleGenerate}}}, []FileInfo{NewVideoFileInfo(input)})
	task.Status = PipelineStatusFailed
	task.ErrorMessage = "published NFO identity mismatch"
	require.NoError(t, store.CreateTask(ctx, task))
	require.NoError(t, store.UpdateTask(ctx, task))
	work, err := subtitle.RecordingWorkPath(library, "42", task.ID, 0, input, "Host", at)
	require.NoError(t, err)
	video := filepath.Join(library, "Host", "Season 01", "Host.S01E1685894400000000.2026-09-05 - Room.mp4")
	stem := strings.TrimSuffix(work, ".mp4")
	checkpoint := subtitle.Metadata{Status: subtitle.StatusCompleted, SourcePath: input, OutputPath: work, SRTPath: stem + ".srt", ASSPath: stem + ".ass", SourcePublicationPath: video, CompletedAt: &at, Segments: []subtitle.Segment{{Text: "字幕"}}, RecordMeta: map[string]any{"live_session_id": "42", "recording_producer_id": origin.ProducerID, "pipeline_task_id": fmt.Sprint(task.ID)}}
	for _, p := range []string{work, checkpoint.SRTPath, checkpoint.ASSPath} {
		require.NoError(t, os.WriteFile(p, []byte("分段"), 0644))
	}
	require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", checkpoint))
	source, err := subtitle.CaptureRecordingSource(library, "42", checkpoint)
	require.NoError(t, err)
	stem = strings.TrimSuffix(video, ".mp4")
	product := subtitle.Metadata{Status: subtitle.StatusCompleted, SourcePath: input, OutputPath: video, SRTPath: stem + ".srt", ASSPath: stem + ".ass", CompletedAt: &at, Segments: checkpoint.Segments, RecordingSources: []subtitle.RecordingSource{source}, RecordMeta: map[string]any{"live_session_id": "42", "live_session_media_role": "aggregate"}}
	nfo, err := subtitle.BuildLibraryEpisodeNFO(video, at, "已发布")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(stem+".nfo", []byte(nfo), 0644))
	for _, p := range []string{video, product.SRTPath, product.ASSPath} {
		require.NoError(t, os.WriteFile(p, []byte("已发布成品"), 0644))
	}
	require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", product))
	require.NoError(t, store.CompleteRecordingTaskMedia(ctx, "42", task.ID, []SessionMediaSource{{TaskID: task.ID, ProducerID: origin.ProducerID, InputPath: input, LibraryPath: work, MetadataPath: strings.TrimSuffix(work, ".mp4") + ".subtitle.json"}}))
	if len(completedSibling) > 0 && completedSibling[0] {
		input := filepath.Join(filepath.Dir(task.CurrentFiles[0].Path), "second.mp4")
		require.NoError(t, os.WriteFile(input, []byte("第二个原片"), 0644))
		owner := NewPipelineTask(task.RecordInfo, task.PipelineConfig, []FileInfo{NewVideoFileInfo(input)})
		require.NoError(t, store.CreateTask(ctx, owner))
		owner.Status = PipelineStatusCompleted
		owner.CurrentStage = len(owner.PipelineConfig.Stages)
		require.NoError(t, store.UpdateTask(ctx, owner))
		work, err := subtitle.RecordingWorkPath(configs.GetCurrentConfig().Subtitle.LibraryRoot, "42", owner.ID, 0, input, "Host", task.RecordInfo.StartTime)
		require.NoError(t, err)
		meta, err := subtitle.LoadMetadata(product.RecordingSources[0].MetadataPath)
		require.NoError(t, err)
		meta.SourcePath, meta.OutputPath = input, work
		stem := strings.TrimSuffix(work, ".mp4")
		meta.SRTPath, meta.ASSPath = stem+".srt", stem+".ass"
		meta.RecordMeta["pipeline_task_id"] = fmt.Sprint(owner.ID)
		for _, p := range []string{work, meta.SRTPath, meta.ASSPath} {
			require.NoError(t, os.WriteFile(p, []byte("已烧录"), 0644))
		}
		require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", meta))
		source, err := subtitle.CaptureRecordingSource(configs.GetCurrentConfig().Subtitle.LibraryRoot, "42", meta)
		require.NoError(t, err)
		product.RecordingSources = append(product.RecordingSources, source)
		require.NoError(t, subtitle.SaveMetadata(strings.TrimSuffix(product.OutputPath, ".mp4")+".subtitle.json", product))
		require.NoError(t, store.CompleteRecordingTaskMedia(ctx, "42", owner.ID, []SessionMediaSource{{TaskID: owner.ID, ProducerID: owner.RecordInfo.RecordingProducerID, InputPath: input, LibraryPath: work, MetadataPath: stem + ".subtitle.json"}}))
		require.NoError(t, os.Remove(input))
	}
	require.NoError(t, store.EndRecordingSession(ctx, "room", "normal"))
	require.NoError(t, store.FinishRecordingProducer(ctx, origin, ""))
	require.NoError(t, os.Remove(input))
	m := NewManager(ctx, store, nil, nil)
	require.NoError(t, m.validateTaskResume(task), "既有正式续跑必须已能证明缺失原片被成品覆盖")
	return m, store, task, product
}

func TestAutomaticRecoveryResumesPublishedMissingSource(t *testing.T) {
	m, store, task, product := publishedRecoveryFixture(t)
	m.compensateLibraryFailures()
	got, err := m.GetTask(task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LibraryRecovery, "已发布原片不应阻止自动发现")
	require.Equal(t, "waiting", got.LibraryRecovery.State)
	// 真实关闭并重开SQLite，证明不是只在内存中保留发布证据。
	require.NoError(t, store.Close())
	store, err = NewSQLiteStore(filepath.Join(filepath.Dir(configs.GetCurrentConfig().Subtitle.LibraryRoot), "pipeline.db"))
	require.NoError(t, err)
	defer store.Close()
	m = NewManager(context.Background(), store, nil, nil)
	m.compensateLibraryFailures()
	got, err = m.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusPending, got.Status)
	require.Equal(t, task.CurrentFiles, got.CurrentFiles)
	require.Equal(t, task.StageResults, got.StageResults)
	require.NoFileExists(t, task.CurrentFiles[0].Path)
	data, err := os.ReadFile(product.OutputPath)
	require.NoError(t, err)
	require.Equal(t, "已发布成品", string(data))
	// 发布前正式门禁也要通过；只替换只读索引观察，不替换文件校验。
	library := configs.GetCurrentConfig().Subtitle.LibraryRoot
	writePublishedRecoveryCapability(t, library)
	m.SetLibraryIndexObserver(fixtureIndexObserver{})
	require.NoError(t, m.EnsureLibraryPublication(context.Background(), LibraryPublicationRequest{TaskID: task.ID, LibraryRoot: library, Path: product.OutputPath, SessionID: "42", RecordedAt: task.RecordInfo.StartTime, Phase: "before"}))
}

func writePublishedRecoveryCapability(t *testing.T, library string) {
	t.Helper()
	state, err := subtitle.CaptureLibraryFileState(library, false)
	require.NoError(t, err)
	capability := map[string]any{"schema_version": 2, "canonical_library_root": library, "library_dev": state.Device, "library_inode": state.Inode, "service_uid": os.Geteuid(), "service_gid": os.Getegid(), "mode": "exclusive_application_nfo_writer", "platform_build_path": subtitle.LibraryPlatformBuildPath, "platform_build_sha256": strings.Repeat("b", 64), "installed_at": time.Now().Format(time.RFC3339Nano), "probe_evidence_sha256": strings.Repeat("a", 64), "library_attributes": state}
	require.NoError(t, os.MkdirAll(subtitle.LibraryRecoveryRoot(library), 0700))
	encoded, err := json.Marshal(capability)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(subtitle.LibraryRecoveryRoot(library), "capability.json"), encoded, 0600))
}

type heldPublicationIndexObserver struct {
	mapping LibraryIndexMapping
}

func (o heldPublicationIndexObserver) VerifyPublication(ctx context.Context, expected LibraryIndexExpected) (LibraryIndexObservation, error) {
	result, err := (fixtureIndexObserver{}).VerifyPublication(ctx, expected)
	result.Mappings = []LibraryIndexMapping{o.mapping}
	return result, err
}

func TestExplicitHoldRetainsCapturedPublicationBaseline(t *testing.T) {
	m, store, task, product := publishedRecoveryFixture(t)
	ctx := context.Background()
	library := configs.GetCurrentConfig().Subtitle.LibraryRoot
	writePublishedRecoveryCapability(t, library)
	// 旧 NFO 缺少稳定身份，正式 before 必须进入原位修复并把索引基线写入任务记录。
	stem := strings.TrimSuffix(product.OutputPath, ".mp4")
	product.RecordMeta["start_time"] = task.RecordInfo.StartTime.Format(time.RFC3339Nano)
	require.NoError(t, subtitle.SaveMetadata(stem+".subtitle.json", product))
	require.NoError(t, os.WriteFile(stem+".nfo", []byte("<episodedetails><season>1</season><episode>1</episode></episodedetails>"), 0644))

	at := task.RecordInfo.StartTime.Add(24 * time.Hour)
	identity := (at.UnixMicro() - 1577836800000000) * 8
	season := filepath.Dir(product.OutputPath)
	req := LibraryPublicationRequest{
		TaskID: task.ID, LibraryRoot: library, Path: filepath.Join(season, fmt.Sprintf("Host.S01E%d.%s - Next.mp4", identity, at.Format("2006-01-02"))),
		SessionID: task.RecordInfo.LiveSessionID, RecordedAt: at, Phase: "before",
	}
	mapping := LibraryIndexMapping{Path: product.OutputPath, FileID: "11", EpisodeID: "12", ShowID: "13", SeasonID: "14", CategoryID: "15", LibraryID: "16", Ordinal: 1, SeasonNumber: 1}
	m.SetLibraryIndexObserver(heldPublicationIndexObserver{mapping: mapping})
	due := at.Add(time.Hour)
	task.Status, task.NotBefore, task.ErrorMessage = PipelineStatusPending, &due, ""
	require.NoError(t, store.UpdateTask(ctx, task))
	held, err := m.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, "deadline", held.LibraryRecovery.State)
	require.False(t, held.LibraryRecovery.BeforeCaptured)
	_, err = subtitle.BuildLibraryEpisodeNFO(req.Path, req.RecordedAt, "")
	require.ErrorContains(t, err, "published NFO identity mismatch")

	unlock, err := subtitle.LockLibraryPublicationContext(ctx, req.Path)
	require.NoError(t, err)
	err = m.EnsureLibraryPublication(ctx, req)
	unlock()
	require.NoError(t, err)
	captured, err := m.GetTask(task.ID)
	require.NoError(t, err)
	baseline := captured.LibraryRecovery
	require.Equal(t, "deadline", baseline.State)
	require.True(t, baseline.BeforeCaptured)
	require.Equal(t, []LibraryIndexMapping{mapping}, baseline.Expected.Protected)

	task.Status, task.ErrorMessage = PipelineStatusFailed, "published NFO identity mismatch"
	require.NoError(t, store.UpdateTask(ctx, task))
	m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Now: func() time.Time { return due }})
	m.compensateLibraryFailures()

	require.NoError(t, store.Close())
	reopened, err := NewSQLiteStore(filepath.Join(filepath.Dir(library), "pipeline.db"))
	require.NoError(t, err)
	t.Cleanup(func() { reopened.Close() })
	got, err := NewManager(ctx, reopened, nil, nil).GetTask(task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LibraryRecovery)
	t.Logf("持久保护映射数 %d → %d，恢复状态 %s → %s", len(baseline.Expected.Protected), len(got.LibraryRecovery.Expected.Protected), baseline.State, got.LibraryRecovery.State)
	require.Equal(t, []LibraryIndexMapping{mapping}, got.LibraryRecovery.Expected.Protected)
	require.True(t, got.LibraryRecovery.BeforeCaptured)
	require.Equal(t, baseline, got.LibraryRecovery)
	require.Equal(t, PipelineStatusFailed, got.Status)
	require.True(t, got.NotBefore.Equal(due))
}

func TestPublishedMissingSourceRejectsUnprovenDiscovery(t *testing.T) {
	for _, damage := range []string{"no_ledger", "wrong_producer", "wrong_session", "no_nfo", "missing_product", "empty_subtitle", "unconfirmed", "cancelled"} {
		t.Run(damage, func(t *testing.T) {
			m, store, task, product := publishedRecoveryFixture(t)
			switch damage {
			case "no_ledger":
				product.RecordingSources = nil
			case "wrong_producer":
				product.RecordingSources[0].ProducerID = "other"
			case "wrong_session":
				product.RecordMeta["live_session_id"] = "other"
			case "no_nfo":
				require.NoError(t, os.Remove(strings.TrimSuffix(product.OutputPath, ".mp4")+".nfo"))
			case "missing_product":
				require.NoError(t, os.Remove(product.OutputPath))
			case "empty_subtitle":
				require.NoError(t, os.WriteFile(product.SRTPath, nil, 0644))
			case "unconfirmed":
				product.Status = subtitle.StatusRunning
			case "cancelled":
				require.NoError(t, m.CancelTask(task.ID))
			}
			require.NoError(t, subtitle.SaveMetadata(strings.TrimSuffix(product.OutputPath, ".mp4")+".subtitle.json", product))
			m.compensateLibraryFailures()
			got, err := m.GetTask(task.ID)
			require.NoError(t, err)
			require.Nil(t, got.LibraryRecovery)
			require.NotEqual(t, PipelineStatusPending, got.Status)
			records, err := store.libraryRecoveries(context.Background())
			require.NoError(t, err)
			require.Empty(t, records)
		})
	}
}

func TestPublishedMissingSourceEvidenceChangesPreventResumeAndPublication(t *testing.T) {
	for _, damage := range []string{"source_reappeared", "source_symlink", "parent_symlink", "product", "nfo", "srt", "ass", "metadata", "checkpoint"} {
		t.Run(damage, func(t *testing.T) {
			m, _, task, product := publishedRecoveryFixture(t)
			m.compensateLibraryFailures()
			got, err := m.GetTask(task.ID)
			require.NoError(t, err)
			require.NotNil(t, got.LibraryRecovery)
			input := task.CurrentFiles[0].Path
			stem := strings.TrimSuffix(product.OutputPath, ".mp4")
			paths := map[string]string{"product": product.OutputPath, "nfo": stem + ".nfo", "srt": product.SRTPath, "ass": product.ASSPath, "metadata": stem + ".subtitle.json", "checkpoint": product.RecordingSources[0].MetadataPath}
			switch damage {
			case "source_reappeared":
				require.NoError(t, os.WriteFile(input, []byte("新原片不得删除"), 0644))
			case "source_symlink":
				require.NoError(t, os.Symlink(input+"-missing", input))
			case "parent_symlink":
				parent := filepath.Dir(input)
				require.NoError(t, os.Rename(parent, parent+"-moved"))
				require.NoError(t, os.Symlink(parent+"-moved", parent))
			default:
				p := paths[damage]
				data, err := os.ReadFile(p)
				require.NoError(t, err)
				// 同inode、同长度并还原mtime，验证小文件依靠内容SHA而非仅stat。
				info, err := os.Stat(p)
				require.NoError(t, err)
				data[0] ^= 1
				if damage == "product" {
					data = append(data, 1) // 媒体沿用revision校验，不宣称全片SHA。
				}
				require.NoError(t, os.WriteFile(p, data, 0644))
				if damage != "product" {
					require.NoError(t, os.Chtimes(p, info.ModTime(), info.ModTime()))
				}
			}
			m.compensateLibraryFailures()
			got, err = m.GetTask(task.ID)
			require.NoError(t, err)
			require.Equal(t, PipelineStatusFailed, got.Status)
			want := "protected_source_changed"
			switch damage {
			case "source_reappeared", "source_symlink":
				want = "published_source_reappeared_or_unreadable"
			case "parent_symlink":
				want = "published_source_parent_changed"
			}
			require.ErrorContains(t, m.EnsureLibraryPublication(context.Background(), LibraryPublicationRequest{TaskID: task.ID, LibraryRoot: configs.GetCurrentConfig().Subtitle.LibraryRoot, Path: product.OutputPath, SessionID: "42", RecordedAt: task.RecordInfo.StartTime, Phase: "before"}), want)
		})
	}
}

func TestPublishedMissingSourcePreservesHoldAndCancellation(t *testing.T) {
	for _, scenario := range []string{"due", "cancelled", "disabled", "extended", "ordinary_failure", "compute_deadline", "next_window", "missing_proof"} {
		t.Run(scenario, func(t *testing.T) {
			m, store, task, product := publishedRecoveryFixture(t)
			ctx := context.Background()
			now := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)
			due := now.Add(time.Hour)
			if scenario == "ordinary_failure" {
				task.ErrorMessage = "non-publication failure"
			}
			if scenario == "next_window" {
				cfg := configs.GetCurrentConfig()
				cfg.Subtitle.Schedule = configs.SubtitleScheduleConfig{Enabled: true, RunAt: "10:00"}
			}
			if scenario == "missing_proof" {
				require.NoError(t, os.Remove(product.OutputPath))
			}
			task.NotBefore = &due
			require.NoError(t, store.UpdateTask(ctx, task))
			if scenario == "compute_deadline" {
				tx, err := store.db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer tx.Rollback()
				require.NoError(t, saveCompletionDeadlineTx(ctx, tx, task.ID, &due, &due, "compute_due", due))
				require.NoError(t, tx.Commit())
			}
			m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Now: func() time.Time { return now }})
			got, err := m.GetTask(task.ID)
			require.NoError(t, err)
			holdOnly := got.LibraryRecovery
			require.NotNil(t, holdOnly)
			require.Equal(t, "deadline", holdOnly.State)
			require.Equal(t, "deadline", holdOnly.RecoveryClass)
			require.True(t, holdOnly.Deadline.ExplicitHold.Equal(due))

			m.compensateLibraryFailures()
			got, err = m.GetTask(task.ID)
			require.NoError(t, err)
			require.Equal(t, PipelineStatusFailed, got.Status)
			if scenario == "ordinary_failure" || scenario == "missing_proof" || scenario == "compute_deadline" {
				now = due.Add(2 * time.Hour)
				m.compensateLibraryFailures()
				got, err = m.GetTask(task.ID)
				require.NoError(t, err)
				require.Equal(t, PipelineStatusFailed, got.Status)
				require.Equal(t, holdOnly, got.LibraryRecovery)
				return
			}
			waiting := got.LibraryRecovery
			require.Equal(t, "waiting", waiting.State)
			require.Equal(t, holdOnly.Revision+1, waiting.Revision)
			require.Equal(t, holdOnly.Deadline, waiting.Deadline)
			require.Equal(t, []string{task.CurrentFiles[0].Path}, waiting.PublishedMissingSources)
			require.NotEmpty(t, waiting.ProtectedSources)

			now = due.Add(-time.Second)
			m.compensateLibraryFailures()
			got, err = m.GetTask(task.ID)
			require.NoError(t, err)
			require.Equal(t, PipelineStatusFailed, got.Status)
			require.True(t, got.NotBefore.Equal(due))
			require.Equal(t, waiting.Revision, got.LibraryRecovery.Revision)

			now = due.Add(time.Second)
			switch scenario {
			case "cancelled":
				require.NoError(t, m.CancelTask(task.ID))
				m.compensateLibraryFailures()
				got, err = m.GetTask(task.ID)
				require.NoError(t, err)
				require.Equal(t, PipelineStatusCancelled, got.Status)
				require.True(t, got.NotBefore.Equal(due))
				return
			case "disabled":
				configs.GetCurrentConfig().Subtitle.Enabled = false
				m.compensateLibraryFailures()
				got, err = m.GetTask(task.ID)
				require.NoError(t, err)
				require.Equal(t, PipelineStatusFailed, got.Status)
				require.Equal(t, waiting.Revision, got.LibraryRecovery.Revision)
				return
			case "extended":
				extended := due.Add(time.Hour)
				m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Now: func() time.Time { return now }, Boundary: func(point string) error {
					if point == "resume_requested" {
						current, err := store.GetTask(ctx, task.ID)
						require.NoError(t, err)
						current.NotBefore = &extended
						require.NoError(t, store.UpdateTask(ctx, current))
					}
					return nil
				}})
				m.compensateLibraryFailures()
				got, err = m.GetTask(task.ID)
				require.NoError(t, err)
				require.Equal(t, PipelineStatusFailed, got.Status)
				require.True(t, got.NotBefore.Equal(extended))
				require.True(t, got.LibraryRecovery.Deadline.ExplicitHold.Equal(extended))
				require.Equal(t, waiting.ProtectedSources, got.LibraryRecovery.ProtectedSources)
				due = extended
				now = extended
				m.SetLibraryRecoveryHooks(LibraryRecoveryHooks{Now: func() time.Time { return now }})
			case "next_window":
				window := due.Add(time.Hour)
				require.True(t, waiting.NextAttempt.Equal(window))
				m.compensateLibraryFailures()
				got, err = m.GetTask(task.ID)
				require.NoError(t, err)
				require.Equal(t, PipelineStatusFailed, got.Status)
				require.Equal(t, waiting.Revision, got.LibraryRecovery.Revision)
				now = window
			}
			m.compensateLibraryFailures()
			got, err = m.GetTask(task.ID)
			require.NoError(t, err)
			require.Equal(t, PipelineStatusPending, got.Status)
			require.False(t, got.NotBefore.Before(due))
			require.Equal(t, "resumed", got.LibraryRecovery.State)
			require.Equal(t, waiting.ProtectedSources, got.LibraryRecovery.ProtectedSources)
			t.Logf("持久状态 %s → %s → %s，任务 %s", holdOnly.State, waiting.State, got.LibraryRecovery.State, got.Status)
		})
	}
}

func TestPublishedMissingSourceIncludesCompletedSibling(t *testing.T) {
	m, store, task, _ := publishedRecoveryFixture(t, true)
	m.compensateLibraryFailures()
	m.compensateLibraryFailures()
	got, err := m.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusPending, got.Status)
	unchanged, err := store.GetTask(context.Background(), task.ID+1)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusCompleted, unchanged.Status)
}

func TestPublishedMissingSourceDirectResumeRechecksProof(t *testing.T) {
	m, _, task, _ := publishedRecoveryFixture(t)
	m.compensateLibraryFailures()
	require.NoError(t, os.WriteFile(task.CurrentFiles[0].Path, []byte("新的原片"), 0644))
	require.Error(t, m.ResumeTask(task.ID), "正式直接续跑也必须保护已冻结的缺源事实")
	got, err := m.GetTask(task.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusFailed, got.Status)
}

func TestPublishedMissingSourceSerializesSiblingProofCapture(t *testing.T) {
	m, store, first, product := publishedRecoveryFixture(t, true)
	ctx := context.Background()
	second, err := store.GetTask(ctx, first.ID+1)
	require.NoError(t, err)
	second.Status, second.CurrentStage = PipelineStatusFailed, 0
	second.ErrorMessage = first.ErrorMessage
	require.NoError(t, store.UpdateTask(ctx, second))
	m.compensateLibraryFailures() // 第一条冻结证明。
	m.compensateLibraryFailures() // 第一条正式续跑。
	m.compensateLibraryFailures() // 第一条仍pending，第二条不得提前冻结。
	got, err := m.GetTask(second.ID)
	require.NoError(t, err)
	require.Nil(t, got.LibraryRecovery)
	// 首任务正常知识同步会更新全部分段的状态；下一任务必须在其结束后取新证明。
	for _, source := range product.RecordingSources {
		meta, err := subtitle.LoadMetadata(source.MetadataPath)
		require.NoError(t, err)
		meta.KnowledgeSyncStatus = subtitle.StatusQueued
		require.NoError(t, subtitle.SaveMetadata(source.MetadataPath, meta))
	}
	first, err = store.GetTask(ctx, first.ID)
	require.NoError(t, err)
	first.Status, first.CurrentStage = PipelineStatusCompleted, 1
	require.NoError(t, store.UpdateTask(ctx, first))
	m.compensateLibraryFailures()
	m.compensateLibraryFailures()
	got, err = m.GetTask(second.ID)
	require.NoError(t, err)
	require.Equal(t, PipelineStatusPending, got.Status)
}
