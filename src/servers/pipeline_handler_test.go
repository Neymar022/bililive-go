package servers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func TestPipelineTaskDetailExposesOnlyRecoveryStatus(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pipeline.db")
	store, err := pipeline.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	manager := pipeline.NewManager(ctx, store, nil, nil)
	t.Cleanup(func() { manager.Close(ctx) })
	task := pipeline.NewPipelineTask(pipeline.RecordInfo{}, &pipeline.PipelineConfig{}, nil)
	require.NoError(t, store.CreateTask(ctx, task))
	record := pipeline.LibraryRecoveryRecord{
		OperationID:    "fixture-journal",
		Kind:           "files",
		Season:         "/internal-fixture",
		TaskID:         task.ID,
		State:          "blocked",
		ReasonCode:     "completion_blocked",
		Reason:         "internal event details",
		Plan:           &subtitle.LibraryIdentityPlan{Members: []subtitle.LibraryIdentityMember{{Before: []byte("internal NFO bytes")}}},
		Expected:       pipeline.LibraryIndexExpected{Path: "/internal-fixture/media.mp4"},
		CompletionRefs: []pipeline.CompletionRef{{RequestPath: "/internal-fixture/request.json", Request: subtitle.WorkerCompletionRequest{Schema: 1}}},
	}
	record.Revision = 1
	record.PlanHash = record.Plan.Hash()
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO pipeline_library_recoveries (operation_id, kind, season, task_id, state, active, revision, record_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		record.OperationID, record.Kind, record.Season, record.TaskID, record.State, 1, 1, raw)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	internal, err := manager.GetTask(task.ID)
	require.NoError(t, err)
	require.NotNil(t, internal.LibraryRecovery.Plan)
	require.False(t, internal.LibraryRecovery.InvalidJournal)
	router := mux.NewRouter()
	router.HandleFunc("/api/pipeline/tasks/{id}", makePipelineGetTaskHandler(manager))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pipeline/tasks/"+strconv.FormatInt(task.ID, 10), nil))
	require.Equal(t, http.StatusOK, response.Code)
	var detail struct {
		ID       int64           `json:"id"`
		Recovery json.RawMessage `json:"library_recovery"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &detail))
	require.Equal(t, task.ID, detail.ID)
	require.NotContains(t, response.Body.String(), "/internal-fixture")
	require.JSONEq(t, `{"state":"blocked","reason_code":"completion_blocked"}`, string(detail.Recovery))
}
