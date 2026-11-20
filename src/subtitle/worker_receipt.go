package subtitle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/google/uuid"
)

// WorkerCompletionRequest 是与可选 Python completion_v1 相同的完整前置对象。
type WorkerCompletionOwner struct {
	TaskID     int64  `json:"task_id"`
	SessionID  string `json:"session_id"`
	ProducerID string `json:"producer_id"`
	InputIndex int    `json:"input_index"`
}
type WorkerCompletionRequest struct {
	Schema           int                   `json:"schema"`
	RequestID        string                `json:"request_id"`
	Owner            WorkerCompletionOwner `json:"owner"`
	SourcePath       string                `json:"source_path"`
	FormalOutputPath string                `json:"formal_output_path"`
	AttemptDirectory string                `json:"attempt_directory"`
	OutputVideoPath  string                `json:"output_video_path"`
	OutputSRTPath    string                `json:"output_srt_path"`
	OutputASSPath    string                `json:"output_ass_path"`
	RecordMeta       map[string]any        `json:"record_meta"`
	RequestSnapshot  map[string]any        `json:"request_snapshot"`
}
type WorkerFileIdentity struct {
	Type         string `json:"type"`
	SharedRootID string `json:"shared_root_id"`
	RelativePath string `json:"relative_path"`
	Inode        uint64 `json:"inode"`
	Size         int64  `json:"size"`
	MtimeNS      int64  `json:"mtime_ns"`
}
type WorkerCompletionReceipt struct {
	Schema           int                           `json:"schema"`
	Request          WorkerCompletionRequest       `json:"request"`
	ExecutedRequest  map[string]any                `json:"executed_request"`
	Response         ProcessResponse               `json:"response"`
	SourceIdentity   WorkerFileIdentity            `json:"source_identity"`
	OutputIdentities map[string]WorkerFileIdentity `json:"output_identities"`
	CompletedAt      time.Time                     `json:"completed_at"`
	ReceiptCreatedAt time.Time                     `json:"receipt_created_at"`
}

// WorkerSubmissionGate 只统计当前进程中心 POST，不声称远端 writer 已排空。
type WorkerSubmissionGate struct {
	mu        sync.Mutex
	accepting bool
	inflight  int
}

var processSubmissionGate = &WorkerSubmissionGate{accepting: true}

func ConfigureWorkerSubmissions(accepting bool) {
	processSubmissionGate.mu.Lock()
	defer processSubmissionGate.mu.Unlock()
	processSubmissionGate.accepting = accepting
}
func WorkerSubmissionsStatus() (bool, int) {
	processSubmissionGate.mu.Lock()
	defer processSubmissionGate.mu.Unlock()
	return processSubmissionGate.accepting, processSubmissionGate.inflight
}
func CheckWorkerSubmissions() error {
	a, _ := WorkerSubmissionsStatus()
	if !a {
		return ErrWorkerSubmissionsClosed
	}
	return nil
}

var ErrWorkerCompletionUnsupported = errors.New("completion_v1_unsupported_platform")

var ErrWorkerSubmissionsClosed = errors.New("worker_submissions_closed: 本地尚未提交")

func enterWorkerSubmission() (func(), error) {
	g := processSubmissionGate
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.accepting {
		return nil, ErrWorkerSubmissionsClosed
	}
	g.inflight++
	return func() { g.mu.Lock(); g.inflight--; g.mu.Unlock() }, nil
}

func CompletionV1Enabled() bool { return os.Getenv("BILILIVE_WORKER_COMPLETION_V1") == "true" }
func VerifyWorkerCompletionCapability(workerURL string) error {
	if !WorkerCompletionSupported() {
		return ErrWorkerCompletionUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(workerURL, "/")+"/healthz", nil)
	if err != nil {
		return errors.New("completion_capability_unavailable")
	}
	response, err := newWorkerHTTPClient().Do(request)
	if err != nil {
		return errors.New("completion_capability_unavailable")
	}
	defer response.Body.Close()
	var body struct {
		Status     string `json:"status"`
		Completion struct {
			Supported    bool   `json:"supported"`
			Enabled      bool   `json:"enabled"`
			Schema       int    `json:"schema"`
			IdentityMode string `json:"identity_mode"`
		} `json:"completion_v1"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&body) != nil || body.Status != "ok" || !body.Completion.Supported || !body.Completion.Enabled || body.Completion.Schema != 1 || body.Completion.IdentityMode != "shared-root-relative-inode" {
		return errors.New("completion_capability_required")
	}
	return nil
}
func receiptJSONEqual(a, b any) bool {
	x, e := json.Marshal(a)
	if e != nil {
		return false
	}
	y, e := json.Marshal(b)
	if e != nil {
		return false
	}
	var av, bv any
	if json.Unmarshal(x, &av) != nil || json.Unmarshal(y, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
func CompletionJSONEqual(a, b any) bool { return receiptJSONEqual(a, b) }

func completionRoot() (string, string, error) {
	root := os.Getenv("SUBTITLE_COMPLETION_SHARED_ROOT")
	id := os.Getenv("SUBTITLE_COMPLETION_SHARED_ROOT_ID")
	if root == "" || id == "" || !filepath.IsAbs(root) {
		return "", "", errors.New("completion_shared_root_unconfigured")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != filepath.Clean(root) || resolved == string(filepath.Separator) {
		return "", "", errors.New("completion_shared_root_invalid")
	}
	return resolved, id, nil
}
func completionPath(path string) error {
	root, _, err := completionRoot()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("completion_path_invalid")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("completion_path_outside_root")
	}
	p := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, component)
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("completion_symlink_path")
		}
	}
	return nil
}
func ValidateCompletionRequest(r WorkerCompletionRequest) error {
	if !WorkerCompletionSupported() {
		return ErrWorkerCompletionUnsupported
	}
	if r.Schema != 1 || r.RequestID == "" || r.Owner.TaskID <= 0 || r.Owner.SessionID == "" || r.Owner.ProducerID == "" || r.Owner.InputIndex < 0 {
		return errors.New("completion_owner_invalid")
	}
	if _, err := uuid.Parse(r.RequestID); err != nil {
		return errors.New("completion_request_id_invalid")
	}
	for _, p := range []string{r.SourcePath, r.FormalOutputPath, r.AttemptDirectory, r.OutputVideoPath, r.OutputSRTPath, r.OutputASSPath} {
		if err := completionPath(p); err != nil {
			return err
		}
	}
	if filepath.Dir(r.AttemptDirectory) != filepath.Dir(r.FormalOutputPath) || !strings.HasPrefix(filepath.Base(r.AttemptDirectory), ".attempt-") || filepath.Dir(r.OutputVideoPath) != r.AttemptDirectory || filepath.Base(r.OutputVideoPath) != filepath.Base(r.FormalOutputPath) || filepath.Ext(r.FormalOutputPath) != ".mp4" || r.OutputSRTPath != strings.TrimSuffix(r.OutputVideoPath, ".mp4")+".srt" || r.OutputASSPath != strings.TrimSuffix(r.OutputVideoPath, ".mp4")+".ass" {
		return errors.New("completion_attempt_mismatch")
	}
	info, err := os.Lstat(r.AttemptDirectory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || r.SourcePath == r.FormalOutputPath || r.SourcePath == r.OutputVideoPath || r.SourcePath == r.OutputSRTPath || r.SourcePath == r.OutputASSPath {
		return errors.New("completion_attempt_invalid")
	}
	if r.RecordMeta["pipeline_task_id"] != fmt.Sprint(r.Owner.TaskID) || r.RecordMeta["live_session_id"] != r.Owner.SessionID || r.RecordMeta["recording_producer_id"] != r.Owner.ProducerID || r.RecordMeta["recording_attempt_path"] != r.OutputVideoPath {
		return errors.New("completion_record_owner_mismatch")
	}
	return nil
}
func DurableCompletionJSON(path string, value any) error {
	if err := completionPath(path); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".completion-go-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(temporary, path); err != nil {
		if !os.IsExist(err) {
			return err
		}
		existing, e := os.ReadFile(path)
		if e != nil || !receiptJSONEqual(json.RawMessage(existing), value) {
			return errors.New("completion_proof_conflict")
		}
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func PrepareCompletionRequest(req ProcessRequest, formal string, owner WorkerCompletionOwner) (*WorkerCompletionRequest, error) {
	req.BurnStyle.Preset = req.BurnStyle.GetEffectivePreset()
	r := &WorkerCompletionRequest{Schema: 1, RequestID: uuid.NewString(), Owner: owner, SourcePath: req.SourcePath, FormalOutputPath: formal, AttemptDirectory: filepath.Dir(req.OutputVideoPath), OutputVideoPath: req.OutputVideoPath, OutputSRTPath: req.OutputSRTPath, OutputASSPath: strings.TrimSuffix(req.OutputSRTPath, ".srt") + ".ass", RecordMeta: req.RecordMeta, RequestSnapshot: map[string]any{"provider": req.Provider, "language": req.Language, "burn_style": req.BurnStyle}}
	if err := ValidateCompletionRequest(*r); err != nil {
		return nil, err
	}
	if err := DurableCompletionJSON(filepath.Join(r.AttemptDirectory, "worker-request.json"), r); err != nil {
		return nil, err
	}
	return r, nil
}

func completionRead(path string, target any) error {
	if err := completionPath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("completion_proof_not_private")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.UseNumber()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("completion_proof_trailing_data")
	}
	return nil
}
func CompletionFileIdentity(path string) (WorkerFileIdentity, error) {
	var out WorkerFileIdentity
	if err := completionPath(path); err != nil {
		return out, err
	}
	root, id, err := completionRoot()
	if err != nil {
		return out, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return out, errors.New("completion_file_invalid")
	}
	inode, err := workerCompletionInode(info)
	if err != nil {
		return out, err
	}
	rel, _ := filepath.Rel(root, path)
	return WorkerFileIdentity{"regular", id, rel, inode, info.Size(), info.ModTime().UnixNano()}, nil
}
func ReadWorkerCompletion(expected WorkerCompletionRequest) (*WorkerCompletionReceipt, error) {
	if err := ValidateCompletionRequest(expected); err != nil {
		return nil, err
	}
	var request WorkerCompletionRequest
	if err := completionRead(filepath.Join(expected.AttemptDirectory, "worker-request.json"), &request); err != nil {
		return nil, err
	}
	if !receiptJSONEqual(request, expected) {
		return nil, errors.New("completion_request_changed")
	}
	var receipt WorkerCompletionReceipt
	if err := completionRead(filepath.Join(expected.AttemptDirectory, "worker-result.json"), &receipt); err != nil {
		return nil, errors.New("completion_unknown")
	}
	if receipt.Schema != 1 || !receiptJSONEqual(receipt.Request, expected) || receipt.CompletedAt.IsZero() || receipt.ReceiptCreatedAt.IsZero() {
		return nil, errors.New("completion_receipt_mismatch")
	}
	var started struct {
		Schema          int                     `json:"schema"`
		Request         WorkerCompletionRequest `json:"request"`
		ExecutedRequest map[string]any          `json:"executed_request"`
		SourceIdentity  WorkerFileIdentity      `json:"source_identity"`
	}
	if err := completionRead(filepath.Join(expected.AttemptDirectory, "worker-started.json"), &started); err != nil || started.Schema != 1 || !receiptJSONEqual(started.Request, expected) || !receiptJSONEqual(started.ExecutedRequest, receipt.ExecutedRequest) || started.SourceIdentity != receipt.SourceIdentity {
		return nil, errors.New("completion_started_mismatch")
	}
	// v1 使用 Python 请求的缺省语义；它的旧 preset 默认与 Go 的有效 preset 不同。
	normalizedStyle := configs.NewConfig().Subtitle.BurnStyle
	normalizedStyle.Preset = "bottom_center"
	styleJSON, err := json.Marshal(expected.RequestSnapshot["burn_style"])
	if err != nil || json.Unmarshal(styleJSON, &normalizedStyle) != nil {
		return nil, errors.New("completion_snapshot_invalid")
	}
	expectedExecution := map[string]any{"source_path": expected.SourcePath, "output_video_path": expected.OutputVideoPath, "output_srt_path": expected.OutputSRTPath, "provider": expected.RequestSnapshot["provider"], "language": expected.RequestSnapshot["language"], "burn_style": normalizedStyle}
	for key, value := range expectedExecution {
		if !receiptJSONEqual(receipt.ExecutedRequest[key], value) {
			return nil, errors.New("completion_executed_request_mismatch")
		}
	}
	source, err := CompletionFileIdentity(expected.SourcePath)
	if err != nil || source != receipt.SourceIdentity {
		return nil, errors.New("completion_source_changed")
	}
	for key, path := range map[string]string{"video": expected.OutputVideoPath, "srt": expected.OutputSRTPath, "ass": expected.OutputASSPath} {
		actual, err := CompletionFileIdentity(path)
		if err != nil || actual != receipt.OutputIdentities[key] {
			return nil, errors.New("completion_output_changed")
		}
	}
	if len(receipt.Response.Segments) == 0 || receipt.Response.ASSPath != expected.OutputASSPath || receipt.Response.RenderPreset == "" || receipt.Response.ActualProvider == "" || receipt.Response.ActualModel == "" || receipt.Response.ActualBurnProvider == "" {
		return nil, errors.New("completion_response_partial")
	}
	for _, segment := range receipt.Response.Segments {
		if segment.Start == "" || segment.End == "" || segment.Text == "" {
			return nil, errors.New("completion_response_partial")
		}
	}
	return &receipt, nil
}
