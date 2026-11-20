package subtitle

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

// 首片行为先经正式公共调用，不依赖尚未存在的生产类型声明。
func TestWorkerDeliveryDirectRefusedCarriesBoundEvidence(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	request := ProcessRequest{SourcePath: "/isolated/source.mp4", OutputVideoPath: "/isolated/.attempt-owned/output.mp4", RecordMeta: map[string]any{"pipeline_task_id": "4101", "recording_producer_id": "owned", "live_session_id": "isolated"}}
	_, err = ProcessFileWithRetry(endpoint, request, 1)
	if err == nil {
		t.Fatal("关闭的隔离监听端口必须失败")
	}
	var evidence interface{ WorkerRequestNotSent(ProcessRequest) bool }
	if !errors.As(err, &evidence) || !evidence.WorkerRequestNotSent(request) {
		t.Fatalf("首次直连refused未提供本次请求绑定的严格未发送证据: %T %v", err, err)
	}
	changed := request
	changed.OutputVideoPath = "/isolated/.attempt-another/output.mp4"
	if evidence.WorkerRequestNotSent(changed) {
		t.Fatal("本次证明不能用于另一个attempt")
	}
}

func deliveryRequest() ProcessRequest {
	return ProcessRequest{SourcePath: "/owned/source.mp4", OutputVideoPath: "/owned/.attempt-4101/output.mp4", RecordMeta: map[string]any{"pipeline_task_id": "4101", "recording_producer_id": "owner", "live_session_id": "session"}}
}

func deliveryTransport(t *testing.T, dial func(context.Context, string, string) (net.Conn, error), proxy func(*http.Request) (*url.URL, error)) *workerProcessTransport {
	t.Helper()
	transport := newWorkerProcessTransport(dial, proxy)
	previous := workerProcessTransportForCall
	workerProcessTransportForCall = func() *workerProcessTransport { return transport }
	t.Cleanup(func() { workerProcessTransportForCall = previous; transport.standard.CloseIdleConnections() })
	return transport
}

func deliveryDNS() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "isolated.invalid", IsNotFound: true}}
}
func deliveryRefused() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
}
func directDelivery(*http.Request) (*url.URL, error) { return nil, nil }

func TestWorkerDeliveryExactErrorChains(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		safe  bool
	}{
		{"DNS", deliveryDNS(), true}, {"refused", deliveryRefused(), true},
		{"temporary", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{IsNotFound: true, IsTemporary: true}}, false},
		{"DNS_timeout", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{IsNotFound: true, IsTimeout: true}}, false},
		{"connect_timeout", &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ETIMEDOUT}}, false},
		{"cancel", context.Canceled, false}, {"deadline", context.DeadlineExceeded, false}, {"EOF", io.EOF, false},
		{"reset", &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNRESET}}, false},
		{"read", &net.OpError{Op: "read", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}, false},
		{"UDP", &net.OpError{Op: "dial", Net: "udp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}, false},
		{"wrong_syscall", &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "write", Err: syscall.ECONNREFUSED}}, false},
		{"forged_text", errors.New("dial tcp: lookup: no such host; connect: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deliveryTransport(t, func(context.Context, string, string) (net.Conn, error) { return nil, tc.cause }, directDelivery)
			req := deliveryRequest()
			_, err := ProcessFileWithRetry("http://isolated.invalid", req, 1)
			if err == nil || IsWorkerRequestNotSent(err, req) != tc.safe {
				t.Fatalf("safe=%v, actual=%T %v", tc.safe, err, err)
			}
		})
	}
}

func TestWorkerDeliveryReceivedRedirectRemainsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		failure error
	}{
		{"307_DNS", 307, deliveryDNS()}, {"308_DNS", 308, deliveryDNS()}, {"307_refused", 307, deliveryRefused()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					t.Errorf("method=%s", r.Method)
				}
				body, _ := io.ReadAll(r.Body)
				if len(body) == 0 {
					t.Error("POST正文为空")
				}
				posts.Add(1)
				w.Header().Set("Connection", "close")
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			var dials atomic.Int32
			deliveryTransport(t, func(ctx context.Context, n, a string) (net.Conn, error) {
				if dials.Add(1) == 1 {
					return (&net.Dialer{}).DialContext(ctx, n, a)
				}
				return nil, tc.failure
			}, directDelivery)
			req := deliveryRequest()
			_, err := ProcessFileWithRetry(server.URL, req, 1)
			var proof *WorkerRequestNotSentError
			if err == nil || IsWorkerRequestNotSent(err, req) || errors.As(err, &proof) || posts.Load() != 1 || dials.Load() != 2 {
				t.Fatalf("posts=%d dials=%d err=%T %v", posts.Load(), dials.Load(), err, err)
			}
		})
	}
}

type deliveryRoundTripper func(*http.Request) (*http.Response, error)

func (f deliveryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkerDeliveryProxyAndUntrustedTransport(t *testing.T) {
	t.Run("actual_proxy_callback", func(t *testing.T) {
		var calls int
		deliveryTransport(t, func(context.Context, string, string) (net.Conn, error) { return nil, deliveryRefused() }, func(*http.Request) (*url.URL, error) { calls++; return url.Parse("http://127.0.0.1:9") })
		req := deliveryRequest()
		_, err := ProcessFileWithRetry("http://isolated.invalid", req, 1)
		if calls != 1 || IsWorkerRequestNotSent(err, req) {
			t.Fatalf("proxy calls=%d err=%v", calls, err)
		}
	})
	t.Run("custom_transport", func(t *testing.T) {
		previous := workerProcessTransportForCall
		defer func() { workerProcessTransportForCall = previous }()
		workerProcessTransportForCall = func() *workerProcessTransport {
			return &workerProcessTransport{roundTripper: deliveryRoundTripper(func(*http.Request) (*http.Response, error) { return nil, deliveryDNS() })}
		}
		req := deliveryRequest()
		_, err := ProcessFileWithRetry("http://isolated.invalid", req, 1)
		if IsWorkerRequestNotSent(err, req) {
			t.Fatal("custom transport不可认证")
		}
	})
	t.Run("global_DefaultTransport_is_not_trusted", func(t *testing.T) {
		var received atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, `{}`) }))
		defer server.Close()
		previous := http.DefaultTransport
		defer func() { http.DefaultTransport = previous }()
		http.DefaultTransport = deliveryRoundTripper(func(*http.Request) (*http.Response, error) {
			t.Error("process使用了可替换DefaultTransport")
			return nil, deliveryDNS()
		})
		_, err := ProcessFileWithRetry(server.URL, deliveryRequest(), 1)
		if err != nil || received.Load() != 1 {
			t.Fatalf("received=%d err=%v", received.Load(), err)
		}
	})
}

func TestWorkerDeliveryWholeOuterCall(t *testing.T) {
	for _, first := range []string{"EOF", "HTTP500", "prior_proof", "unknown_then_prior_proof"} {
		t.Run(first, func(t *testing.T) {
			req := deliveryRequest()
			deliveryTransport(t, func(context.Context, string, string) (net.Conn, error) { return nil, deliveryDNS() }, directDelivery)
			_, prior := ProcessFileWithRetry("http://isolated.invalid", req, 1)
			if !IsWorkerRequestNotSent(prior, req) {
				t.Fatal("前置独立证明无效")
			}
			var dials atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Connection", "close")
				w.WriteHeader(500)
			}))
			defer server.Close()
			deliveryTransport(t, func(ctx context.Context, n, a string) (net.Conn, error) {
				if dials.Add(1) == 1 {
					switch first {
					case "EOF", "unknown_then_prior_proof":
						return nil, io.EOF
					case "prior_proof":
						return nil, prior
					case "HTTP500":
						return (&net.Dialer{}).DialContext(ctx, n, a)
					}
				}
				if first == "unknown_then_prior_proof" {
					return nil, prior
				}
				return nil, deliveryDNS()
			}, directDelivery)
			_, err := ProcessFileWithRetry(server.URL, req, 2)
			var typed *WorkerRequestNotSentError
			if err == nil || IsWorkerRequestNotSent(err, req) || errors.As(err, &typed) || dials.Load() != 2 {
				t.Fatalf("whole call err=%T %v dials=%d", err, err, dials.Load())
			}
		})
	}
	t.Run("all_direct_failures", func(t *testing.T) {
		deliveryTransport(t, func(context.Context, string, string) (net.Conn, error) { return nil, deliveryDNS() }, directDelivery)
		req := deliveryRequest()
		_, err := ProcessFileWithRetry("http://isolated.invalid", req, 2)
		if !IsWorkerRequestNotSent(err, req) {
			t.Fatalf("全部未发送仍应认证: %v", err)
		}
	})
}

func TestWorkerDeliveryResponsesAndLossAreUnknown(t *testing.T) {
	for _, kind := range []string{"received_then_EOF", "partial_JSON", "HTTP500", "HTTP400", "success"} {
		t.Run(kind, func(t *testing.T) {
			var received atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				received.Add(1)
				switch kind {
				case "received_then_EOF":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						conn.Close()
					}
				case "partial_JSON":
					io.WriteString(w, `{"segments":`)
				case "HTTP500":
					w.WriteHeader(500)
				case "HTTP400":
					w.WriteHeader(400)
				default:
					io.WriteString(w, `{}`)
				}
			}))
			defer server.Close()
			req := deliveryRequest()
			_, err := ProcessFileWithRetry(server.URL, req, 1)
			if received.Load() != 1 || IsWorkerRequestNotSent(err, req) || (kind != "success" && err == nil) {
				t.Fatalf("received=%d err=%v", received.Load(), err)
			}
		})
	}
}

type deliveryFailWriteConn struct {
	net.Conn
	fail *atomic.Bool
}

func (c *deliveryFailWriteConn) Write(p []byte) (int, error) {
	if c.fail.CompareAndSwap(true, false) {
		return 0, io.ErrUnexpectedEOF
	}
	return c.Conn.Write(p)
}
func TestWorkerDeliveryReusedConnectionRedialUnknown(t *testing.T) {
	var posts, dials atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		posts.Add(1)
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	deliveryTransport(t, func(ctx context.Context, n, a string) (net.Conn, error) {
		if dials.Add(1) > 1 {
			return nil, deliveryDNS()
		}
		c, err := (&net.Dialer{}).DialContext(ctx, n, a)
		if err != nil {
			return nil, err
		}
		return &deliveryFailWriteConn{Conn: c, fail: &fail}, nil
	}, directDelivery)
	req := deliveryRequest()
	if _, err := ProcessFileWithRetry(server.URL, req, 1); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	_, err := ProcessFileWithRetry(server.URL, req, 1)
	if err == nil || IsWorkerRequestNotSent(err, req) || posts.Load() != 1 || dials.Load() != 2 {
		t.Fatalf("posts=%d dials=%d err=%v", posts.Load(), dials.Load(), err)
	}
}

type deliveryForgedError struct{}

func (deliveryForgedError) Error() string                            { return "worker request not sent (v1 reason=dns_not_found)" }
func (deliveryForgedError) WorkerRequestNotSent(ProcessRequest) bool { return true }
func TestWorkerDeliveryProofCannotBeForgedOrLeakPayload(t *testing.T) {
	req := deliveryRequest()
	for _, err := range []error{&WorkerRequestNotSentError{}, deliveryForgedError{}, errors.New("dial tcp: connection refused")} {
		if IsWorkerRequestNotSent(err, req) {
			t.Fatal("伪造证明获准")
		}
	}
	deliveryTransport(t, func(context.Context, string, string) (net.Conn, error) { return nil, deliveryDNS() }, directDelivery)
	req.SourcePath = "secret-body-marker"
	_, err := ProcessFileWithRetry("http://privateuser:privatepassword@isolated.invalid", req, 1)
	if !IsWorkerRequestNotSent(err, req) {
		t.Fatal(err)
	}
	for _, secret := range []string{"privateuser", "privatepassword", "secret-body-marker", "isolated.invalid"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("诊断泄漏%s", secret)
		}
	}
}

func TestWorkerDeliveryRealTimeoutAndTLSRemainUnknown(t *testing.T) {
	t.Run("response_timeout", func(t *testing.T) {
		t.Setenv("SUBTITLE_WORKER_TIMEOUT", "25ms")
		var received atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			received.Add(1)
			<-r.Context().Done()
		}))
		defer server.Close()
		req := deliveryRequest()
		_, err := ProcessFileWithRetry(server.URL, req, 1)
		if err == nil || IsWorkerRequestNotSent(err, req) || received.Load() != 1 {
			t.Fatalf("received=%d err=%v", received.Load(), err)
		}
	})
	t.Run("default_TLS_rejects_untrusted_certificate", func(t *testing.T) {
		var received atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
		defer server.Close()
		req := deliveryRequest()
		_, err := ProcessFileWithRetry(server.URL, req, 1)
		if err == nil || IsWorkerRequestNotSent(err, req) || received.Load() != 0 {
			t.Fatalf("TLS默认验证失效或错误获准: %v", err)
		}
	})
}

func TestWorkerDeliveryLateDialAndTraceCannotUpgradeEvidence(t *testing.T) {
	t.Run("dial_still_running_after_Do_timeout", func(t *testing.T) {
		t.Setenv("SUBTITLE_WORKER_TIMEOUT", "15ms")
		release, finished := make(chan struct{}), make(chan struct{})
		deliveryTransport(t, func(context.Context, string, string) (net.Conn, error) {
			<-release
			close(finished)
			return nil, deliveryDNS()
		}, directDelivery)
		req := deliveryRequest()
		_, err := ProcessFileWithRetry("http://isolated.invalid", req, 1)
		if err == nil || IsWorkerRequestNotSent(err, req) {
			t.Fatal("未完成dial不得获准")
		}
		close(release)
		<-finished
		if IsWorkerRequestNotSent(err, req) {
			t.Fatal("迟到dial不得把既有unknown升级为证明")
		}
	})
	t.Run("successful_ConnectDone_is_sticky", func(t *testing.T) {
		deliveryTransport(t, func(ctx context.Context, network, address string) (net.Conn, error) {
			// 在被批准的可审受控dial边界模拟标准Dialer的成功connect事件。
			trace := httptrace.ContextClientTrace(ctx)
			trace.ConnectDone(network, address, nil)
			return nil, deliveryDNS()
		}, directDelivery)
		req := deliveryRequest()
		_, err := ProcessFileWithRetry("http://isolated.invalid", req, 1)
		if err == nil || IsWorkerRequestNotSent(err, req) {
			t.Fatal("成功连接后DNS不得获准")
		}
	})
}
