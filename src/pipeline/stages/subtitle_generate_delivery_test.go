package stages

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

func deliveryStageFixture(t *testing.T, endpoint string, ctx context.Context) (*SubtitleGenerateStage, *pipeline.PipelineContext, []pipeline.FileInfo, string) {
	t.Helper()
	root := t.TempDir()
	library := filepath.Join(root, "video")
	require.NoError(t, os.Mkdir(library, 0755))
	source := filepath.Join(root, "source.mp4")
	require.NoError(t, os.WriteFile(source, []byte("保留原输入"), 0644))
	cfg := configs.NewConfig()
	cfg.Subtitle.Enabled, cfg.Subtitle.LibraryRoot = true, library
	cfg.Subtitle.KnowledgeSync.Enabled = false
	previous := configs.GetCurrentConfig()
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(previous) })
	t.Setenv("SUBTITLE_WORKER_URL", endpoint)
	pc := &pipeline.PipelineContext{Ctx: ctx, TaskID: 4101, RecordInfo: pipeline.RecordInfo{HostName: "Host", LiveSessionID: "delivery-session", RecordingProducerID: "delivery-owner", StartTime: time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)}, SessionMediaReady: func([]pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
		t.Error("未发送任务不能提交ready")
		return pipeline.RecordingSession{}, nil
	}}
	work, err := subtitle.RecordingWorkPath(library, pc.RecordInfo.LiveSessionID, pc.TaskID, 0, source, "Host", pc.RecordInfo.StartTime)
	require.NoError(t, err)
	return &SubtitleGenerateStage{}, pc, []pipeline.FileInfo{pipeline.NewVideoFileInfo(source)}, strings.TrimSuffix(work, ".mp4") + ".subtitle.json"
}

func TestSubtitleGenerateDeliveryDirectRefusedDefers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint := "http://" + listener.Addr().String()
	require.NoError(t, listener.Close())
	stage, pc, input, metaPath := deliveryStageFixture(t, endpoint, context.Background())
	_, err = stage.Execute(pc, input)
	var later *pipeline.RetryLaterError
	require.True(t, errors.As(err, &later), "严格首次直连未发送应延后，实际: %v", err)
	meta, err := subtitle.LoadMetadata(metaPath)
	require.NoError(t, err)
	require.Equal(t, subtitle.StatusQueued, meta.Status)
	require.Equal(t, subtitle.StatusQueued, meta.RendererStatus)
	require.Equal(t, input[0].Path, meta.SourcePath)
	require.Equal(t, "delivery-owner", meta.RecordMeta["recording_producer_id"])
	attempt := meta.RecordMeta["recording_attempt_path"].(string)
	entries, err := os.ReadDir(filepath.Dir(attempt))
	require.NoError(t, err)
	require.Empty(t, entries)
	b, err := os.ReadFile(input[0].Path)
	require.NoError(t, err)
	require.Equal(t, "保留原输入", string(b))
}

// 用真实公共process占住串行闸门，保证stage已保存running而尚未dial；不用替换writer。
func holdDeliveryProcess(t *testing.T) func() {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		io.WriteString(w, `{}`)
	}))
	done := make(chan error, 1)
	go func() { _, err := subtitle.ProcessFileWithRetry(server.URL, subtitle.ProcessRequest{}, 1); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("公共process未进入隔离server")
	}
	unblock := func() { once.Do(func() { close(release); require.NoError(t, <-done); server.Close() }) }
	t.Cleanup(unblock)
	return unblock
}

func closedDeliveryEndpoint(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint := "http://" + listener.Addr().String()
	require.NoError(t, listener.Close())
	return endpoint
}

func TestSubtitleGenerateDeliveryConcurrentCheckpointProtection(t *testing.T) {
	for _, kind := range []string{"metadata_owner", "metadata_result", "attempt_result", "source_changed", "cancelled", "metadata_missing", "metadata_save_denied"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "metadata_save_denied" && os.Geteuid() == 0 {
				t.Skip("实际权限故障需非root用户")
			}
			release := holdDeliveryProcess(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stage, pc, input, metaPath := deliveryStageFixture(t, closedDeliveryEndpoint(t), ctx)
			done := make(chan error, 1)
			go func() { _, err := stage.Execute(pc, input); done <- err }()
			// 只观察本测试私有文件，等待stage到达已保存running的实际边界。
			require.Eventually(t, func() bool {
				m, e := subtitle.LoadMetadata(metaPath)
				return e == nil && m.Status == subtitle.StatusRunning
			}, 5*time.Second, time.Millisecond)
			meta, err := subtitle.LoadMetadata(metaPath)
			require.NoError(t, err)
			var preserved []byte
			switch kind {
			case "metadata_owner":
				meta.RecordMeta["recording_producer_id"] = "new-owner"
				require.NoError(t, subtitle.SaveMetadata(metaPath, meta))
				preserved, err = os.ReadFile(metaPath)
				require.NoError(t, err)
			case "metadata_result":
				meta.Status = subtitle.StatusCompleted
				meta.Segments = []subtitle.Segment{{Text: "新的已保存结果"}}
				require.NoError(t, subtitle.SaveMetadata(metaPath, meta))
				preserved, err = os.ReadFile(metaPath)
				require.NoError(t, err)
			case "attempt_result":
				attempt := meta.RecordMeta["recording_attempt_path"].(string)
				require.NoError(t, os.WriteFile(attempt, []byte("新结果不可覆盖"), 0644))
			case "source_changed":
				require.NoError(t, os.WriteFile(input[0].Path, []byte("新输入不可覆盖"), 0644))
			case "cancelled":
				cancel()
			case "metadata_save_denied":
				parent := filepath.Dir(metaPath)
				require.NoError(t, os.Chmod(parent, 0555))
				t.Cleanup(func() { _ = os.Chmod(parent, 0755) })
			case "metadata_missing":
				require.NoError(t, os.Rename(metaPath, metaPath+".preserved"))
			}
			release()
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("stage未结束")
			}
			var later *pipeline.RetryLaterError
			require.Error(t, err)
			if kind == "metadata_save_denied" {
				require.ErrorContains(t, err, ".checkpoint-")
			}
			require.False(t, errors.As(err, &later), "冲突/取消不得归队: %v", err)
			if preserved != nil {
				actual, e := os.ReadFile(metaPath)
				require.NoError(t, e)
				require.Equal(t, preserved, actual)
			}
			if kind != "metadata_missing" && preserved == nil {
				current, e := subtitle.LoadMetadata(metaPath)
				require.NoError(t, e)
				require.NotEqual(t, subtitle.StatusQueued, current.Status)
			}
			if kind == "attempt_result" {
				b, e := os.ReadFile(meta.RecordMeta["recording_attempt_path"].(string))
				require.NoError(t, e)
				require.Equal(t, "新结果不可覆盖", string(b))
			}
		})
	}
}

func TestSubtitleGenerateDeliveryHistoricalUnknownDoesNotPost(t *testing.T) {
	for id := int64(7101); id <= 7117; id++ {
		t.Run(strconv.FormatInt(id, 10), func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			stage, pc, input, _ := deliveryStageFixture(t, server.URL, context.Background())
			pc.TaskID = id
			cfg := configs.GetCurrentConfig()
			work, err := subtitle.RecordingWorkPath(cfg.Subtitle.LibraryRoot, pc.RecordInfo.LiveSessionID, id, 0, input[0].Path, "Host", pc.RecordInfo.StartTime)
			require.NoError(t, err)
			metaPath := strings.TrimSuffix(work, ".mp4") + ".subtitle.json"
			// 第一次真实stage保存running，server返回unknown；第二次不得因错误文字而重发。
			_, err = stage.Execute(pc, input)
			require.Error(t, err)
			meta, err := subtitle.LoadMetadata(metaPath)
			require.NoError(t, err)
			meta.Status, meta.RendererStatus = subtitle.StatusFailed, subtitle.StatusFailed
			meta.LastError = "dial tcp: lookup: no such host; connect: connection refused"
			require.NoError(t, subtitle.SaveMetadata(metaPath, meta))
			before, err := os.ReadFile(metaPath)
			require.NoError(t, err)
			attemptDir := filepath.Dir(meta.RecordMeta["recording_attempt_path"].(string))
			beforeAttempt, err := os.ReadDir(attemptDir)
			require.NoError(t, err)
			beforeParent, err := os.ReadDir(filepath.Dir(attemptDir))
			require.NoError(t, err)
			count := posts.Load()
			require.EqualValues(t, 1, count)
			_, err = stage.Execute(pc, input)
			require.Error(t, err)
			var later *pipeline.RetryLaterError
			require.False(t, errors.As(err, &later))
			require.Equal(t, count, posts.Load(), "旧unknown不能重算")
			after, err := os.ReadFile(metaPath)
			require.NoError(t, err)
			require.Equal(t, before, after)
			afterAttempt, err := os.ReadDir(attemptDir)
			require.NoError(t, err)
			require.Equal(t, beforeAttempt, afterAttempt)
			afterParent, err := os.ReadDir(filepath.Dir(attemptDir))
			require.NoError(t, err)
			require.Equal(t, beforeParent, afterParent, "拒绝前不得创建新attempt")
		})
	}
}

func TestSubtitleGenerateDeliveryNonSessionAndCancelledEntry(t *testing.T) {
	t.Run("non_session", func(t *testing.T) {
		stage, pc, input, _ := deliveryStageFixture(t, closedDeliveryEndpoint(t), context.Background())
		pc.RecordInfo.LiveSessionID = ""
		pc.RecordInfo.RecordingProducerID = ""
		pc.SessionMediaReady = nil
		configs.GetCurrentConfig().FfmpegPath = fakeFFmpegForCover(t)
		_, err := stage.Execute(pc, input)
		require.Error(t, err)
		require.IsType(t, &subtitle.WorkerRequestNotSentError{}, err, "必须到达真实worker失败，不能让封面准备错误通过")
		var later *pipeline.RetryLaterError
		require.False(t, errors.As(err, &later))
		libraryPath, err := subtitle.ResolveLibraryVideoPath(input[0].Path, configs.GetCurrentConfig().Subtitle.LibraryRoot)
		require.NoError(t, err)
		metadataPath := strings.TrimSuffix(libraryPath, filepath.Ext(libraryPath)) + ".subtitle.json"
		metadata, err := subtitle.LoadMetadata(metadataPath)
		require.NoError(t, err)
		require.Equal(t, subtitle.StatusFailed, metadata.Status)
		require.Equal(t, subtitle.StatusFailed, metadata.RendererStatus)
		require.Equal(t, input[0].Path, metadata.SourcePath)
		require.Equal(t, libraryPath, metadata.OutputPath)
	})
	t.Run("already_cancelled", func(t *testing.T) {
		var posts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1); io.WriteString(w, `{}`) }))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stage, pc, input, metaPath := deliveryStageFixture(t, server.URL, ctx)
		_, err := stage.Execute(pc, input)
		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, posts.Load())
		_, err = os.Stat(metaPath)
		require.True(t, os.IsNotExist(err))
	})
}

func TestSubtitleGenerateDeliveryQueuedAndConfirmedResultPaths(t *testing.T) {
	t.Run("queued_can_defer_again", func(t *testing.T) {
		stage, pc, input, metaPath := deliveryStageFixture(t, closedDeliveryEndpoint(t), context.Background())
		_, err := stage.Execute(pc, input)
		var later *pipeline.RetryLaterError
		require.ErrorAs(t, err, &later)
		first, err := subtitle.LoadMetadata(metaPath)
		require.NoError(t, err)
		oldAttempt := first.RecordMeta["recording_attempt_path"].(string)
		_, err = stage.Execute(pc, input)
		require.ErrorAs(t, err, &later)
		second, err := subtitle.LoadMetadata(metaPath)
		require.NoError(t, err)
		require.Equal(t, subtitle.StatusQueued, second.Status)
		require.NotEqual(t, oldAttempt, second.RecordMeta["recording_attempt_path"])
		entries, err := os.ReadDir(filepath.Dir(oldAttempt))
		require.NoError(t, err)
		require.Empty(t, entries, "旧attempt必须保留")
	})
	for _, kind := range []string{"completed", "failed_with_confirmed_attempt"} {
		t.Run(kind, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			stage, pc, input, metaPath := deliveryStageFixture(t, closedDeliveryEndpoint(t), context.Background())
			_, err := stage.Execute(pc, input)
			var later *pipeline.RetryLaterError
			require.ErrorAs(t, err, &later)
			meta, err := subtitle.LoadMetadata(metaPath)
			require.NoError(t, err)
			meta.Status = subtitle.StatusCompleted
			meta.Segments = []subtitle.Segment{{Text: "已确认结果"}}
			if kind == "completed" {
				delete(meta.RecordMeta, "recording_attempt_path")
			}
			result := meta
			if kind != "completed" {
				attempt := meta.RecordMeta["recording_attempt_path"].(string)
				result.OutputPath = attempt
				result.SRTPath = strings.TrimSuffix(attempt, ".mp4") + ".srt"
				result.ASSPath = strings.TrimSuffix(attempt, ".mp4") + ".ass"
			}
			for _, path := range []string{result.OutputPath, result.SRTPath, result.ASSPath} {
				require.NoError(t, os.WriteFile(path, []byte("已保存成果"), 0644))
			}
			if kind != "completed" {
				require.NoError(t, subtitle.SaveMetadata(strings.TrimSuffix(result.OutputPath, ".mp4")+".subtitle.json", result))
				meta.Status = subtitle.StatusFailed
				meta.Segments = nil
			}
			require.NoError(t, subtitle.SaveMetadata(metaPath, meta))
			t.Setenv("SUBTITLE_WORKER_URL", server.URL)
			ready := 0
			sentinel := errors.New("仅观察已确认结果领取边界")
			pc.SessionMediaReady = func(sources []pipeline.SessionMediaSource) (pipeline.RecordingSession, error) {
				ready++
				require.Len(t, sources, 1)
				return pipeline.RecordingSession{}, sentinel
			}
			_, err = stage.Execute(pc, input)
			require.ErrorIs(t, err, sentinel)
			require.Equal(t, 1, ready)
			require.Zero(t, posts.Load())
			confirmed, err := subtitle.LoadMetadata(metaPath)
			require.NoError(t, err)
			require.Equal(t, subtitle.StatusCompleted, confirmed.Status)
			for _, path := range []string{confirmed.OutputPath, confirmed.SRTPath, confirmed.ASSPath} {
				b, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "已保存成果", string(b))
			}
		})
	}
}

func TestSubtitleGenerateDeliveryRunningUnknownDoesNotPost(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	stage, pc, input, metaPath := deliveryStageFixture(t, server.URL, context.Background())
	_, err := stage.Execute(pc, input)
	require.Error(t, err)
	meta, err := subtitle.LoadMetadata(metaPath)
	require.NoError(t, err)
	meta.Status, meta.RendererStatus = subtitle.StatusRunning, subtitle.StatusRunning
	meta.LastError = "dial tcp: lookup: no such host"
	require.NoError(t, subtitle.SaveMetadata(metaPath, meta))
	before, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	attemptDir := filepath.Dir(meta.RecordMeta["recording_attempt_path"].(string))
	oldEntries, err := os.ReadDir(filepath.Dir(attemptDir))
	require.NoError(t, err)
	_, err = stage.Execute(pc, input)
	require.ErrorContains(t, err, "completion is unconfirmed")
	require.EqualValues(t, 1, posts.Load())
	after, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	require.Equal(t, before, after)
	newEntries, err := os.ReadDir(filepath.Dir(attemptDir))
	require.NoError(t, err)
	require.Equal(t, oldEntries, newEntries)
}
