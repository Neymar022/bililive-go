package subtitle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
)

// WorkerHTTPError 表示字幕 worker 返回的非 2xx 响应。
// 区分 4xx（客户端错，重试无意义）和 5xx（服务端错，可重试）需要结构化 error，
// 否则只能在调用方做字符串匹配 "status 500"，又脆又丑。
type WorkerHTTPError struct {
	StatusCode int
	Body       string
	Code       string
}

func (e *WorkerHTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("subtitle worker returned status %d", e.StatusCode)
	}
	return fmt.Sprintf("subtitle worker returned status %d: %s", e.StatusCode, e.Body)
}

type ProcessRequest struct {
	CompletionV1    *WorkerCompletionRequest  `json:"completion_v1,omitempty"`
	SourcePath      string                    `json:"source_path"`
	OutputVideoPath string                    `json:"output_video_path"`
	OutputSRTPath   string                    `json:"output_srt_path"`
	Provider        string                    `json:"provider"`
	Language        string                    `json:"language"`
	BurnStyle       configs.SubtitleBurnStyle `json:"burn_style"`
	RecordMeta      map[string]any            `json:"record_meta,omitempty"`
}

type ProcessResponse struct {
	Segments           []Segment `json:"segments,omitempty"`
	ASSPath            string    `json:"ass_path,omitempty"`
	RenderPreset       string    `json:"render_preset,omitempty"`
	ActualProvider     string    `json:"actual_provider,omitempty"`
	ActualModel        string    `json:"actual_model,omitempty"`
	ActualBurnProvider string    `json:"actual_burn_provider,omitempty"`
}

const macTranscriberUnavailableCode = "mac_transcriber_unavailable"

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
	response, _, err := processFileAttempt(workerURL, req)
	return response, err
}

// WorkerRequestNotSentError 只能由完整调用的可信传输观测生成；零值不构成证明。
// Error仅输出请求摘要及本地调用编号，原始cause只经显式Unwrap供诊断。
type WorkerRequestNotSentError struct {
	proof *workerDeliveryProof
	cause error
}

type workerDeliveryProof struct {
	scope      uint64
	requestSHA [32]byte
	attempts   []*workerDeliveryObservation
	reason     string
}

func (e *WorkerRequestNotSentError) Error() string {
	if e == nil || e.proof == nil {
		return "unverified worker delivery error"
	}
	return fmt.Sprintf("worker request not sent (v1 scope=%d request_sha256=%x reason=%s)", e.proof.scope, e.proof.requestSHA, e.proof.reason)
}
func (e *WorkerRequestNotSentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}
func (e *WorkerRequestNotSentError) WorkerRequestNotSent(req ProcessRequest) bool {
	if e == nil || e.proof == nil || e.proof.scope == 0 || len(e.proof.attempts) == 0 {
		return false
	}
	body, err := workerProcessBody(req)
	if err != nil || sha256.Sum256(body) != e.proof.requestSHA {
		return false
	}
	for _, attempt := range e.proof.attempts {
		if !attempt.certified(e.proof.requestSHA) {
			return false
		}
	}
	return true
}

// IsWorkerRequestNotSent 同时核私有证明和本次source/owner/attempt请求摘要。
func IsWorkerRequestNotSent(err error, req ProcessRequest) bool {
	var proof *WorkerRequestNotSentError
	return errors.As(err, &proof) && proof.WorkerRequestNotSent(req)
}

type workerDeliveryContextKey struct{}
type workerDeliveryObservation struct {
	mu                                  sync.Mutex
	trusted, disqualified               bool
	proxyCalls, dials, finished, active int
	dialErr                             error
	reason                              string
	requestSHA                          [32]byte
}

func (o *workerDeliveryObservation) reject() { o.mu.Lock(); o.disqualified = true; o.mu.Unlock() }
func (o *workerDeliveryObservation) certified(requestSHA [32]byte) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.requestSHA == requestSHA && o.trusted && !o.disqualified && o.proxyCalls == 1 && o.dials == 1 && o.finished == 1 && o.active == 0 && o.reason != ""
}
func workerObservation(ctx context.Context) *workerDeliveryObservation {
	o, _ := ctx.Value(workerDeliveryContextKey{}).(*workerDeliveryObservation)
	return o
}

type workerProcessTransport struct {
	standard     *http.Transport
	roundTripper http.RoundTripper
}

// 私有标准Transport固定默认TLS、代理和池配置；生产只传标准Dialer。
// 工厂只供同包测试在系统边界控制dial，不读取生产配置开关。
func newWorkerProcessTransport(dial func(context.Context, string, string) (net.Conn, error), proxy func(*http.Request) (*url.URL, error)) *workerProcessTransport {
	tr := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			route, err := proxy(req)
			if o := workerObservation(req.Context()); o != nil {
				o.mu.Lock()
				o.proxyCalls++
				o.disqualified = o.disqualified || route != nil || err != nil
				o.mu.Unlock()
			}
			return route, err
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			o := workerObservation(ctx)
			if o != nil {
				o.mu.Lock()
				o.dials++
				o.active++
				o.mu.Unlock()
			}
			conn, err := dial(ctx, network, address)
			if o != nil {
				o.mu.Lock()
				o.finished++
				o.active--
				o.dialErr = err
				o.disqualified = o.disqualified || conn != nil || err == nil
				o.mu.Unlock()
			}
			return conn, err
		},
		ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
	}
	return &workerProcessTransport{standard: tr, roundTripper: tr}
}

var privateWorkerProcessTransport = newWorkerProcessTransport((&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext, http.ProxyFromEnvironment)
var workerProcessTransportForCall = func() *workerProcessTransport { return privateWorkerProcessTransport }
var workerDeliveryScope atomic.Uint64

func workerProcessBody(req ProcessRequest) ([]byte, error) {
	req.BurnStyle.Preset = req.BurnStyle.GetEffectivePreset()
	return json.Marshal(req)
}

func workerDialFailure(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	u, ok := err.(*url.Error)
	if !ok || u.Op != "Post" || u.Timeout() {
		return ""
	}
	n, ok := u.Err.(*net.OpError)
	if !ok || n.Op != "dial" || (n.Net != "tcp" && n.Net != "tcp4" && n.Net != "tcp6") || n.Timeout() {
		return ""
	}
	switch cause := n.Err.(type) {
	case *net.DNSError:
		if cause.IsNotFound && !cause.IsTimeout && !cause.IsTemporary {
			return "dns_not_found"
		}
	case *os.SyscallError:
		if cause.Syscall == "connect" && errors.Is(cause.Err, syscall.ECONNREFUSED) {
			return "connect_refused"
		}
	}
	return ""
}

func processFileAttempt(workerURL string, req ProcessRequest) (ProcessResponse, *workerDeliveryObservation, error) {
	var response ProcessResponse
	done, gateErr := enterWorkerSubmission()
	if gateErr != nil {
		return response, &workerDeliveryObservation{}, gateErr
	}
	defer done()
	o := &workerDeliveryObservation{}
	body, err := workerProcessBody(req)
	if err != nil {
		return response, o, err
	}
	o.requestSHA = sha256.Sum256(body)
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(workerURL, "/")+"/api/v1/process", bytes.NewReader(body))
	if err != nil {
		return response, o, err
	}
	request.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(request.Context(), workerDeliveryContextKey{}, o)
	trace := &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { o.reject() }, WroteHeaders: o.reject,
		WroteRequest: func(httptrace.WroteRequestInfo) { o.reject() }, GotFirstResponseByte: o.reject,
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				o.reject()
			}
		},
	}
	request = request.WithContext(httptrace.WithClientTrace(ctx, trace))
	transport := workerProcessTransportForCall()
	if transport == nil {
		return response, o, errors.New("worker process transport unavailable")
	}
	o.trusted = transport.standard != nil && transport.roundTripper == transport.standard
	client := &http.Client{Transport: transport.roundTripper, Timeout: subtitleWorkerTimeout(), CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		o.reject()
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}}
	httpResponse, err := client.Do(request)
	if err != nil {
		// 远端/自定义 transport 不能透传本地 gate 的未提交证明。
		if errors.Is(err, ErrWorkerSubmissionsClosed) {
			return response, o, workerOutcomeUnknownError{}
		}
		reason := workerDialFailure(err)
		o.mu.Lock()
		// 证据必须来自本次私有dial，不能接纳外部构造的相同错误链。
		if u, ok := err.(*url.Error); ok && o.dialErr != nil && u.Err == o.dialErr {
			o.reason = reason
		}
		o.mu.Unlock()
		return response, o, err
	}
	o.reject()
	defer httpResponse.Body.Close()
	response, err = readWorkerResponse[ProcessResponse](httpResponse)
	return response, o, err
}

// 聚合已经unknown时，禁止通过最后错误链透传其他调用的证明。
type workerOutcomeUnknownError struct{}

func (workerOutcomeUnknownError) Error() string { return "worker request outcome unknown" }
func withoutWorkerDeliveryProof(err error) error {
	var proof *WorkerRequestNotSentError
	if errors.As(err, &proof) {
		return workerOutcomeUnknownError{}
	}
	return err
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

	if req.CompletionV1 != nil {
		maxAttempts = 1
	}
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	body, marshalErr := workerProcessBody(req)
	if marshalErr != nil {
		return ProcessResponse{}, marshalErr
	}
	proof := &workerDeliveryProof{scope: workerDeliveryScope.Add(1), requestSHA: sha256.Sum256(body)}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, observed, err := processFileAttempt(workerURL, req)
		if err == nil {
			return resp, nil
		}
		if errors.Is(err, ErrWorkerSubmissionsClosed) {
			return ProcessResponse{}, err
		}
		lastErr = err
		proof.attempts = append(proof.attempts, observed)
		if !isRetryableError(err) {
			break
		}
		if attempt < maxAttempts {
			time.Sleep(retryBackoff(attempt))
		}
	}
	certified := true
	for _, observed := range proof.attempts {
		if !observed.certified(proof.requestSHA) {
			certified = false
		}
	}
	if certified && len(proof.attempts) == maxAttempts {
		proof.reason = proof.attempts[len(proof.attempts)-1].reason
		return ProcessResponse{}, &WorkerRequestNotSentError{proof: proof, cause: lastErr}
	}
	return ProcessResponse{}, withoutWorkerDeliveryProof(lastErr)
}

// isRetryableError 判断错误是否值得重试。
// 网络/超时/IO 错误一律可重试；
// 5xx 服务端错可重试；
// 4xx 客户端错（参数错、路径错、鉴权错）不重试。
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if IsMacTranscriberUnavailable(err) {
		return false
	}
	var httpErr *WorkerHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 500
	}
	return true
}

func IsMacTranscriberUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *WorkerHTTPError
	if errors.As(err, &httpErr) {
		if httpErr.Code == macTranscriberUnavailableCode {
			return true
		}
		return strings.Contains(httpErr.Body, macTranscriberUnavailableCode)
	}
	return strings.Contains(err.Error(), macTranscriberUnavailableCode)
}

// retryBackoff 返回第 attempt 次失败后的等待时长（attempt 从 1 计）。
// 1 -> 1s，2 -> 2s，3 -> 4s …
func retryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(1<<(attempt-1)) * time.Second
}

func readWorkerResponse[T any](httpResp *http.Response) (T, error) {
	var response T
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxWorkerErrorBodyBytes))
		if len(body) > workerErrorTailBytes {
			body = body[len(body)-workerErrorTailBytes:]
		}
		message := strings.TrimSpace(string(body))
		code := ""
		if message != "" {
			code, message = parseWorkerErrorBody(body, message)
		}
		return response, &WorkerHTTPError{StatusCode: httpResp.StatusCode, Body: message, Code: code}
	}

	if err := json.NewDecoder(httpResp.Body).Decode(&response); err != nil {
		return response, err
	}

	return response, nil
}

func parseWorkerErrorBody(body []byte, fallback string) (string, string) {
	var payload struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fallback
	}

	code := payload.Code
	message := payload.Message
	if len(payload.Detail) > 0 && string(payload.Detail) != "null" {
		var detailString string
		if err := json.Unmarshal(payload.Detail, &detailString); err == nil {
			message = detailString
		} else {
			var detailObject struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(payload.Detail, &detailObject); err == nil {
				if detailObject.Code != "" {
					code = detailObject.Code
				}
				if detailObject.Message != "" {
					message = detailObject.Message
				}
			}
		}
	}
	if message == "" {
		message = fallback
	}
	return code, message
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
