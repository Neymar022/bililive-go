package subtitle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
)

// WorkerHTTPError 表示字幕 worker 返回的非 2xx 响应。
// 区分 4xx（客户端错，重试无意义）和 5xx（服务端错，可重试）需要结构化 error，
// 否则只能在调用方做字符串匹配 "status 500"，又脆又丑。
type WorkerHTTPError struct {
	StatusCode int
	Body       string
}

func (e *WorkerHTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("subtitle worker returned status %d", e.StatusCode)
	}
	return fmt.Sprintf("subtitle worker returned status %d: %s", e.StatusCode, e.Body)
}

type ProcessRequest struct {
	SourcePath      string                    `json:"source_path"`
	OutputVideoPath string                    `json:"output_video_path"`
	OutputSRTPath   string                    `json:"output_srt_path"`
	Provider        string                    `json:"provider"`
	Language        string                    `json:"language"`
	BurnStyle       configs.SubtitleBurnStyle `json:"burn_style"`
	RecordMeta      map[string]any            `json:"record_meta,omitempty"`
}

type ProcessResponse struct {
	Segments     []Segment `json:"segments,omitempty"`
	ASSPath      string    `json:"ass_path,omitempty"`
	RenderPreset string    `json:"render_preset,omitempty"`
}

const defaultSubtitleWorkerTimeout = 4 * time.Hour

const (
	maxWorkerErrorBodyBytes = 1 << 20
	workerErrorTailBytes    = 16 << 10
)

func ResolveRenderPreset(requestedPreset, storedPreset string, style configs.SubtitleBurnStyle) string {
	if requestedPreset != "" {
		return configs.SubtitleBurnStyle{Preset: requestedPreset}.GetEffectivePreset()
	}
	if storedPreset != "" {
		return configs.SubtitleBurnStyle{Preset: storedPreset}.GetEffectivePreset()
	}
	return style.GetEffectivePreset()
}

func ProcessFile(workerURL string, req ProcessRequest) (ProcessResponse, error) {
	req.BurnStyle.Preset = req.BurnStyle.GetEffectivePreset()
	return postToWorker[ProcessResponse](workerURL, "/api/v1/process", req)
}

// DefaultProcessMaxAttempts 是默认重试上限（含首次尝试）。
// 序列 1s+2s 等待覆盖 docker 网络抖动 + worker 短重启窗口；
// 真正坏掉的话 3 次也救不回来，没必要拖更久浪费 pipeline 时间。
const DefaultProcessMaxAttempts = 3

// processSemaphore 是进程级"调用 worker 串行闸门"——容量 1。
//
// 为什么 NAS 端也要节流：
//  1. **配合 Mac 端 GPU 串行**：BiliNote mac_transcriber_service 已经用
//     threading.Semaphore(1) 串行 GPU 访问。NAS 这边也节流后，多个 pipeline
//     goroutine 不会同时开 4h 长连接堆积——一目了然的排队顺序日志。
//  2. **保护 dashscope/local-whisper**：即使没用 Mac，DashScope OSS 上传
//     也不希望 N 个并行（容易 429），local-whisper CPU 也跑不动并发。
//  3. **接近 FIFO 即可**：Go runtime 的 chan 唤醒不保证严格 FIFO，但录制
//     完成时间间隔通常远大于调度抖动（秒 vs 微秒级），实际效果等同 FIFO。
//
// 为什么用 buffered chan 而不是 sync.Mutex：
//   - 未来要加 select-with-timeout（"排队超过 5h 直接 fail，让上层重试调度"）
//     时，chan 直接 select case 即可；Mutex 没有原生 timeout 接口。
//   - 容量 1 → 容量 N 的扩容成本只是改一个数字。
//   - panic-safety：`processSemaphore <- struct{}{}` 紧跟着 `defer <-`，中间
//     没有可能 panic 的代码；即使整段 panic，defer 仍执行（chan recv 不会因
//     panic 丢失），与 Mutex.Unlock defer 等价。
//
// 容量为常量 1：当前所有 provider 在并发场景下都不会更快（GPU 串行、
// CPU 单核加速、OSS 限流），与其分散资源不如让一个任务跑得最快。
var processSemaphore = make(chan struct{}, 1)

// ProcessFileWithRetry 调用 worker 处理文件，对网络错误和 5xx 响应自动重试。
// maxAttempts 是总尝试次数（含首次），< 1 视为 1（不重试）。
// 退避采用指数序列：第 1 次失败后等 1s，第 2 次后 2s，第 3 次后 4s …
// 4xx 客户端错误立即返回（重试同样结果，徒增延迟）。
//
// 入口先抢 processSemaphore——并发 pipeline 下多个 goroutine 在此排队。
// 锁持有期：包括所有 retry 尝试 + 退避等待。这是有意的——一个 task 没彻底
// 完成（不论成功失败）前，不让下一个 task 占用 worker，简化故障定位。
func ProcessFileWithRetry(workerURL string, req ProcessRequest, maxAttempts int) (ProcessResponse, error) {
	processSemaphore <- struct{}{}
	defer func() { <-processSemaphore }()

	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := ProcessFile(workerURL, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryableError(err) {
			return resp, err
		}
		if attempt < maxAttempts {
			time.Sleep(retryBackoff(attempt))
		}
	}
	return ProcessResponse{}, lastErr
}

// isRetryableError 判断错误是否值得重试。
// 网络/超时/IO 错误一律可重试；
// 5xx 服务端错可重试；
// 4xx 客户端错（参数错、路径错、鉴权错）不重试。
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *WorkerHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 500
	}
	return true
}

// retryBackoff 返回第 attempt 次失败后的等待时长（attempt 从 1 计）。
// 1 -> 1s，2 -> 2s，3 -> 4s …
func retryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(1<<(attempt-1)) * time.Second
}

func postToWorker[T any](workerURL string, path string, req any) (T, error) {
	var response T
	body, err := json.Marshal(req)
	if err != nil {
		return response, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(workerURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return response, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := newWorkerHTTPClient()
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return response, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxWorkerErrorBodyBytes))
		if len(body) > workerErrorTailBytes {
			body = body[len(body)-workerErrorTailBytes:]
		}
		message := strings.TrimSpace(string(body))
		if message != "" {
			var payload struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(body, &payload); err == nil && payload.Detail != "" {
				message = payload.Detail
			}
		}
		return response, &WorkerHTTPError{StatusCode: httpResp.StatusCode, Body: message}
	}

	if err := json.NewDecoder(httpResp.Body).Decode(&response); err != nil {
		return response, err
	}

	return response, nil
}

func newWorkerHTTPClient() *http.Client {
	return &http.Client{Timeout: subtitleWorkerTimeout()}
}

func subtitleWorkerTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("SUBTITLE_WORKER_TIMEOUT")); raw != "" {
		if timeout, err := time.ParseDuration(raw); err == nil && timeout > 0 {
			return timeout
		}
	}
	return defaultSubtitleWorkerTimeout
}
