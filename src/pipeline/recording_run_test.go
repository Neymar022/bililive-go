package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/stretchr/testify/require"
)

func startTestCapture(t *testing.T, manager *Manager, liveID, sessionID string) RecordingOrigin {
	t.Helper()
	require.NoError(t, manager.OpenRecordingSession(sessionID, liveID))
	origin, err := manager.BeginRecordingProducer(liveID)
	require.NoError(t, err)
	return origin
}

func TestOnceRecordingCompletionPersistsWithoutWaitingForSubtitles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pipeline.db")
	store, err := NewSQLiteStore(path)
	require.NoError(t, err)
	manager := NewManager(ctx, store, nil, nil)
	run, err := manager.ConfigureRecordingRun("room", configs.RecordingModeOnce)
	require.NoError(t, err)
	require.Equal(t, RecordingRunWaiting, run.State)
	origin := startTestCapture(t, manager, "room", "session-1")
	require.NoError(t, manager.RecordCaptureEvidence(origin, true, ""))
	require.NoError(t, manager.EndRecordingSession("room", "normal"))
	run, err = manager.RecordingRun("room")
	require.NoError(t, err)
	require.Equal(t, RecordingRunFinalizing, run.State)
	require.Error(t, manager.OpenRecordingSession("too-early", "room"))
	require.NoError(t, manager.FinishRecordingProducer(origin, ""))
	completed, err := manager.RecordingRun("room")
	require.NoError(t, err)
	require.Equal(t, RecordingRunCompleted, completed.State)
	require.Equal(t, "once_completed", completed.PauseReason)
	manager.Close(ctx)
	store, err = NewSQLiteStore(path)
	require.NoError(t, err)
	manager = NewManager(ctx, store, nil, nil)
	t.Cleanup(func() { manager.Close(ctx) })
	reloaded, err := manager.ConfigureRecordingRun("room", configs.RecordingModeOnce)
	require.NoError(t, err)
	require.Equal(t, completed, reloaded)
	require.Error(t, manager.OpenRecordingSession("session-2", "room"))
	tasks, err := store.ListTasks(ctx, TaskFilter{})
	require.NoError(t, err)
	require.Empty(t, tasks)
}

func TestRecordingRecoveryUsesCurrentModeAndOnlyPausesUnprovenOnceSessions(t *testing.T) {
	for _, tc := range []struct {
		name           string
		before, after  configs.RecordingMode
		capture, close bool
		want           RecordingRunState
	}{
		{"waiting", configs.RecordingModeOnce, configs.RecordingModeOnce, false, false, RecordingRunWaiting},
		{"recording", configs.RecordingModeOnce, configs.RecordingModeOnce, true, false, RecordingRunPaused},
		{"finalizing", configs.RecordingModeOnce, configs.RecordingModeOnce, true, true, RecordingRunPaused},
		{"continuous", configs.RecordingModeContinuous, configs.RecordingModeContinuous, true, false, RecordingRunWaiting},
		{"changed-to-continuous", configs.RecordingModeOnce, configs.RecordingModeContinuous, true, false, RecordingRunWaiting},
		{"changed-to-once", configs.RecordingModeContinuous, configs.RecordingModeOnce, true, false, RecordingRunPaused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "pipeline.db")
			store, err := NewSQLiteStore(path)
			require.NoError(t, err)
			manager := NewManager(ctx, store, nil, nil)
			_, err = manager.ConfigureRecordingRun("room", tc.before)
			require.NoError(t, err)
			if tc.capture {
				startTestCapture(t, manager, "room", "session")
			}
			if tc.close {
				require.NoError(t, manager.EndRecordingSession("room", "normal"))
			}
			manager.Close(ctx)
			store, err = NewSQLiteStore(path)
			require.NoError(t, err)
			manager = NewManager(ctx, store, nil, nil)
			t.Cleanup(func() { manager.Close(ctx) })
			run, err := manager.ConfigureRecordingRun("room", tc.after)
			require.NoError(t, err)
			require.Equal(t, tc.want, run.State)
			if tc.want == RecordingRunPaused {
				require.Equal(t, "unconfirmed_session_after_restart", run.PauseReason)
				require.Error(t, manager.OpenRecordingSession("next", "room"))
			} else {
				require.NoError(t, manager.OpenRecordingSession("next", "room"))
				repeated, err := manager.ConfigureRecordingRun("room", tc.after)
				require.NoError(t, err)
				require.Equal(t, RecordingRunRecording, repeated.State, "重复绑定不可再次执行启动恢复")
			}
		})
	}
}

func TestRecordingRunUsesLatestModeAndWaitsAgainOnlyForProvenEmptyMedia(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	manager := NewManager(ctx, store, nil, nil)
	t.Cleanup(func() { manager.Close(ctx) })
	run, err := manager.ConfigureRecordingRun("room", configs.RecordingModeOnce)
	require.NoError(t, err)
	for _, tc := range []struct {
		session  string
		mode     configs.RecordingMode
		playable bool
		want     RecordingRunState
	}{
		{"empty", configs.RecordingModeOnce, false, RecordingRunWaiting},
		{"continuous", configs.RecordingModeContinuous, true, RecordingRunWaiting},
		{"once", configs.RecordingModeOnce, true, RecordingRunCompleted},
	} {
		origin := startTestCapture(t, manager, "room", tc.session)
		changed, err := manager.ConfigureRecordingRun("room", tc.mode)
		require.NoError(t, err)
		require.Equal(t, RecordingRunRecording, changed.State)
		require.Equal(t, run.RunID, changed.RunID)
		require.NoError(t, manager.RecordCaptureEvidence(origin, tc.playable, ""))
		require.NoError(t, manager.EndRecordingSession("room", "normal"))
		require.NoError(t, manager.FinishRecordingProducer(origin, ""))
		actual, err := manager.RecordingRun("room")
		require.NoError(t, err)
		require.Equal(t, tc.want, actual.State)
	}
}

func TestManualStopCannotBeUndoneByLateVerificationOrModeChange(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	manager := NewManager(ctx, store, nil, nil)
	t.Cleanup(func() { manager.Close(ctx) })
	run, err := manager.ConfigureRecordingRun("room", configs.RecordingModeOnce)
	require.NoError(t, err)
	origin := startTestCapture(t, manager, "room", "session")
	_, err = manager.RestartRecordingRun("room")
	require.Error(t, err)
	require.NoError(t, manager.EndRecordingSession("room", "user_stop"))
	stopped, err := manager.StopRecordingRun("room")
	require.NoError(t, err)
	require.Equal(t, RecordingRunStopped, stopped.State)
	require.Equal(t, "user_stop", stopped.PauseReason)
	changed, err := manager.ConfigureRecordingRun("room", configs.RecordingModeContinuous)
	require.NoError(t, err)
	require.Equal(t, stopped.State, changed.State)
	require.Error(t, manager.OpenRecordingSession("not-rearmed", "room"))
	next, err := manager.RestartRecordingRun("room")
	require.NoError(t, err)
	require.NotEqual(t, run.RunID, next.RunID)
	startTestCapture(t, manager, "room", "next")
	require.NoError(t, manager.RecordCaptureEvidence(origin, true, ""))
	require.NoError(t, manager.FinishRecordingProducer(origin, ""))
	actual, err := manager.RecordingRun("room")
	require.NoError(t, err)
	require.Equal(t, next.RunID, actual.RunID)
	require.Equal(t, "next", actual.SessionID)
	require.Equal(t, RecordingRunRecording, actual.State)
}

func TestUncertainMediaPausesInsteadOfStartingAnotherOnceSession(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	manager := NewManager(ctx, store, nil, nil)
	t.Cleanup(func() { manager.Close(ctx) })
	_, err = manager.ConfigureRecordingRun("room", configs.RecordingModeOnce)
	require.NoError(t, err)
	origin := startTestCapture(t, manager, "room", "session")
	require.NoError(t, manager.RecordCaptureEvidence(origin, false, "decoder_timeout"))
	require.NoError(t, manager.EndRecordingSession("room", "normal"))
	require.NoError(t, manager.FinishRecordingProducer(origin, ""))
	paused, err := manager.RecordingRun("room")
	require.NoError(t, err)
	require.Equal(t, RecordingRunPaused, paused.State)
	require.Equal(t, "media_verification_failed", paused.PauseReason)
	require.Error(t, manager.OpenRecordingSession("next", "room"))
	reloaded, err := manager.ConfigureRecordingRun("room", configs.RecordingModeOnce)
	require.NoError(t, err)
	require.Equal(t, paused, reloaded)
}

func TestProvenEmptyCaptureWaitsAgainDespitePublicationFailure(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	manager := NewManager(ctx, store, nil, nil)
	t.Cleanup(func() { manager.Close(ctx) })
	_, err = manager.ConfigureRecordingRun("room", configs.RecordingModeOnce)
	require.NoError(t, err)
	origin := startTestCapture(t, manager, "room", "session")
	require.NoError(t, manager.RecordCaptureEvidence(origin, false, ""))
	require.NoError(t, manager.EndRecordingSession("room", "normal"))
	require.NoError(t, manager.FinishRecordingProducer(origin, "recording output was not registered: audio.aac"))
	run, err := manager.RecordingRun("room")
	require.NoError(t, err)
	require.Equal(t, RecordingRunWaiting, run.State)
	require.Empty(t, run.PauseReason)
	session, err := store.RecordingSession(ctx, "session")
	require.NoError(t, err)
	require.False(t, session.Sealed(), "录制额度不应改变发布保护")
	require.NoError(t, manager.OpenRecordingSession("next-session", "room"))
}

func TestRecordingQuotaClosesAfterFinalProducerEvenWhenPublicationFailed(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	manager := NewManager(ctx, store, nil, nil)
	t.Cleanup(func() { manager.Close(ctx) })
	_, err = store.EnsureRecordingRun(ctx, "room", configs.RecordingModeOnce)
	require.NoError(t, err)
	require.NoError(t, manager.OpenRecordingSession("session", "room"))
	first, err := manager.BeginRecordingProducer("room")
	require.NoError(t, err)
	require.NoError(t, manager.RecordCaptureEvidence(first, true, ""))
	require.NoError(t, manager.RecordCaptureEvidence(first, false, "later_attempt_failed"))
	require.NoError(t, manager.FinishRecordingProducer(first, ""))
	require.Error(t, manager.RecordCaptureEvidence(first, false, "late_callback"))
	run, err := store.GetRecordingRun(ctx, "room")
	require.NoError(t, err)
	require.Equal(t, RecordingRunRecording, run.State, "切片不是整场结束")
	last, err := manager.BeginRecordingProducer("room")
	require.NoError(t, err)
	require.NoError(t, manager.EndRecordingSession("room", "normal"))
	run, err = store.GetRecordingRun(ctx, "room")
	require.NoError(t, err)
	require.Equal(t, RecordingRunFinalizing, run.State, "末线程未退出不得消费额度")
	require.NoError(t, manager.RecordCaptureEvidence(last, false, "decoder_timeout"))
	require.NoError(t, manager.FinishRecordingProducer(last, "pipeline registration failed"))
	run, err = store.GetRecordingRun(ctx, "room")
	require.NoError(t, err)
	require.Equal(t, RecordingRunCompleted, run.State, "已有可播放录制不能受字幕或发布失败影响")
	session, err := store.RecordingSession(ctx, "session")
	require.NoError(t, err)
	require.False(t, session.Sealed(), "发布仍需完整输入，不能用单次成功绕过发布保护")
	require.Error(t, manager.OpenRecordingSession("next-session", "room"))
}

func TestLegacyContinuousRecordingDoesNotWaitForPlayableQuotaEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pipeline.db"))
	require.NoError(t, err)
	manager := NewManager(ctx, store, nil, nil)
	t.Cleanup(func() { manager.Close(ctx) })
	_, err = store.EnsureRecordingRun(ctx, "room", "")
	require.NoError(t, err)
	require.NoError(t, manager.OpenRecordingSession("session", "room"))
	producer, err := manager.BeginRecordingProducer("room")
	require.NoError(t, err)
	require.NoError(t, manager.RecordCaptureEvidence(producer, false, "decoder_unavailable"))
	require.NoError(t, manager.EndRecordingSession("room", "normal"))
	require.NoError(t, manager.FinishRecordingProducer(producer, ""))
	run, err := store.GetRecordingRun(ctx, "room")
	require.NoError(t, err)
	require.Equal(t, RecordingRunWaiting, run.State)
	require.Empty(t, run.PauseReason)
	require.NoError(t, manager.OpenRecordingSession("next", "room"))
}
