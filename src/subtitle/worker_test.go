package subtitle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessFileIncludesWorkerDetailOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"detail":"model download failed"}`))
	}))
	t.Cleanup(server.Close)

	_, err := ProcessFile(server.URL, ProcessRequest{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Contains(t, err.Error(), "model download failed")
}

func TestProcessFilePreservesTailOfWorkerErrorDetail(t *testing.T) {
	tail := "final ffmpeg failure line"
	detail := strings.Repeat("x", 7000) + tail
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)
		require.NoError(t, json.NewEncoder(writer).Encode(map[string]string{"detail": detail}))
	}))
	t.Cleanup(server.Close)

	_, err := ProcessFile(server.URL, ProcessRequest{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), tail)
}

func TestProcessFilePreservesFinalizeFailureDetail(t *testing.T) {
	detail := "subtitle finalize failed: publish burned output to /srv/bililive/video.mp4 failed: Device or resource busy"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)
		require.NoError(t, json.NewEncoder(writer).Encode(map[string]string{"detail": detail}))
	}))
	t.Cleanup(server.Close)

	_, err := ProcessFile(server.URL, ProcessRequest{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "subtitle finalize failed")
	assert.Contains(t, err.Error(), "publish burned output")
}

func TestProcessFileSendsEffectiveRenderPreset(t *testing.T) {
	var request ProcessRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		require.NoError(t, json.NewDecoder(req.Body).Decode(&request))
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(ProcessResponse{
			ASSPath: "/tmp/video.ass",
		}))
	}))
	t.Cleanup(server.Close)

	response, err := ProcessFile(server.URL, ProcessRequest{
		BurnStyle: configs.SubtitleBurnStyle{
			Preset: "bottom_center",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, "vizard_classic_cn", request.BurnStyle.Preset)
	assert.Equal(t, "/tmp/video.ass", response.ASSPath)
}

func TestNewWorkerHTTPClientUsesLongerDefaultTimeout(t *testing.T) {
	t.Setenv("SUBTITLE_WORKER_TIMEOUT", "")

	client := newWorkerHTTPClient()

	assert.Equal(t, 4*time.Hour, client.Timeout)
}

func TestProcessFileHonorsConfiguredWorkerTimeout(t *testing.T) {
	t.Setenv("SUBTITLE_WORKER_TIMEOUT", "10ms")

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		time.Sleep(50 * time.Millisecond)
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(ProcessResponse{}))
	}))
	t.Cleanup(server.Close)

	_, err := ProcessFile(server.URL, ProcessRequest{})

	require.Error(t, err)
	assert.ErrorContains(t, err, "Client.Timeout")
}

// 重试相关：服务器先返 5xx 再返成功——重试应该把它救回来。
func TestProcessFileWithRetrySucceedsAfterTransientError(t *testing.T) {
	t.Setenv("SUBTITLE_WORKER_TIMEOUT", "5s")

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		hits++
		if hits == 1 {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"detail":"transient ffmpeg crash"}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(writer).Encode(ProcessResponse{ASSPath: "/tmp/v.ass"}))
	}))
	t.Cleanup(server.Close)

	resp, err := ProcessFileWithRetry(server.URL, ProcessRequest{}, 3)

	require.NoError(t, err)
	assert.Equal(t, 2, hits, "应该正好调用两次：一次失败一次成功")
	assert.Equal(t, "/tmp/v.ass", resp.ASSPath)
}

// 4xx 客户端错误重试同样结果，应当立即放弃，不浪费时间。
func TestProcessFileWithRetryStopsImmediatelyOnClientError(t *testing.T) {
	t.Setenv("SUBTITLE_WORKER_TIMEOUT", "5s")

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		hits++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"detail":"missing source_path"}`))
	}))
	t.Cleanup(server.Close)

	_, err := ProcessFileWithRetry(server.URL, ProcessRequest{}, 3)

	require.Error(t, err)
	assert.Equal(t, 1, hits, "4xx 应该只尝试一次")

	var httpErr *WorkerHTTPError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusBadRequest, httpErr.StatusCode)
}

// 进程级节流：N 个 goroutine 并发调 ProcessFileWithRetry，必须串行进入 worker。
// 验证 chan 信号量按 FIFO 排队，总耗时 ≈ N × 单任务延迟，而不是 1×。
func TestProcessFileWithRetrySerializesConcurrentCalls(t *testing.T) {
	t.Setenv("SUBTITLE_WORKER_TIMEOUT", "5s")

	const perRequestDelay = 200 * time.Millisecond
	const goroutines = 4

	var (
		mu             sync.Mutex
		concurrent     int
		maxConcurrent  int
		successfulHits int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()

		time.Sleep(perRequestDelay)

		mu.Lock()
		concurrent--
		successfulHits++
		mu.Unlock()

		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(ProcessResponse{ASSPath: "/tmp/x.ass"})
	}))
	t.Cleanup(server.Close)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := time.Now()
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = ProcessFileWithRetry(server.URL, ProcessRequest{}, 1)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	assert.Equal(t, goroutines, successfulHits, "所有 goroutine 都应完成")
	assert.Equal(t, 1, maxConcurrent, "任一时刻只能有 1 个请求在 worker 内（被 processSemaphore 串行）")
	// 串行下界：N × delay；放宽 25% 容忍度抵消测试机抖动
	minExpected := time.Duration(goroutines) * perRequestDelay * 75 / 100
	assert.GreaterOrEqual(t, elapsed, minExpected,
		"总耗时应接近 N × delay，确认是串行而非并发")
}

// 全部尝试都失败时，最终返回最后一次的错误，且尝试次数恰好为 maxAttempts。
func TestProcessFileWithRetryExhaustsAttempts(t *testing.T) {
	t.Setenv("SUBTITLE_WORKER_TIMEOUT", "5s")

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		hits++
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`{"detail":"upstream worker offline"}`))
	}))
	t.Cleanup(server.Close)

	start := time.Now()
	_, err := ProcessFileWithRetry(server.URL, ProcessRequest{}, 3)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Equal(t, 3, hits)
	// 退避序列 1s+2s = 3s（不含 RPC 时间）；放宽下限避免 CI 抖动假阴性。
	assert.GreaterOrEqual(t, elapsed, 2900*time.Millisecond, "退避至少 1s+2s")
	assert.ErrorContains(t, err, "502")
}
