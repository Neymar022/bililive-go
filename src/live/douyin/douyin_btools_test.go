package douyin

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBToolsLiveInfoErrorsAreNotOffline(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		code int
		want string
	}{
		{"blocked", `{"error":"所有 API 端点都不可用，请稍后重试"}`, 500, "解析端点持续禁用"},
		{"unknown", `{"error":"Cookie=private-value Authorization=private-value"}`, 500, "请求失败: 500"},
		{"missing-living", `{}`, 200, "缺少 living"},
		{"null-living", `{"living":null}`, 200, "缺少 living"},
		{"malformed", `not-json`, 200, "invalid character"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bgo/live-info" || r.Header.Get("Authorization") != btoolsConsts.authToken {
					t.Errorf("unexpected parser request: %s", r.URL.Path)
				}
				w.WriteHeader(test.code)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			port := btoolsConsts.port
			_, service, _ := net.SplitHostPort(server.Listener.Addr().String())
			btoolsConsts.port, _ = strconv.Atoi(service)
			t.Cleanup(func() { btoolsConsts.port = port })
			parser := btoolsLive{roomId: "123"}
			info, err := parser.GetInfo()
			if err == nil || info != nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("error must stay unknown, not offline, and omit secrets: info=%v err=%v", info, err)
			}
		})
	}
}

func TestBToolsRequestHasTimeout(t *testing.T) {
	if btoolsHTTPClient.Timeout <= 0 || btoolsHTTPClient.Timeout > 30*time.Second {
		t.Fatal("parser requests must have a bounded timeout")
	}
	client := btoolsHTTPClient
	copyClient := *client
	copyClient.Timeout = 20 * time.Millisecond
	btoolsHTTPClient = &copyClient
	t.Cleanup(func() { btoolsHTTPClient = client })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	port := btoolsConsts.port
	_, service, _ := net.SplitHostPort(server.Listener.Addr().String())
	btoolsConsts.port, _ = strconv.Atoi(service)
	t.Cleanup(func() { btoolsConsts.port = port })
	parser := btoolsLive{roomId: "123"}
	info, err := parser.GetInfo()
	if err == nil || info != nil {
		t.Fatal("timeout must not be converted to offline")
	}
}
