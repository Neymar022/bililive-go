package flv

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/live"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/stretchr/testify/require"
)

type stopTestTransport func(*http.Request) (*http.Response, error)

func (f stopTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type stopTestBody struct {
	read func([]byte) (int, error)
}

func (b stopTestBody) Read(p []byte) (int, error) { return b.read(p) }
func (stopTestBody) Close() error                 { return nil }

func TestStopBeforeParseDoesNotStartNativeFLV(t *testing.T) {
	p, err := (&builder{}).Build(nil, livelogger.New(1024, nil))
	require.NoError(t, err)
	require.NoError(t, p.Stop())
	require.NoError(t, p.Stop())
	require.NotPanics(t, func() {
		require.NoError(t, p.ParseLiveStream(context.Background(), nil, nil, ""))
	})
}

func TestNativeFLVStopInterruptsPendingNetworkIO(t *testing.T) {
	for _, headersSent := range []bool{false, true} {
		t.Run(map[bool]string{false: "response_headers", true: "response_body"}[headersSent], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			p, err := (&builder{}).Build(nil, livelogger.New(1024, nil))
			require.NoError(t, err)
			p.(*Parser).hc.Transport = stopTestTransport(func(r *http.Request) (*http.Response, error) {
				wait := func() error {
					close(entered)
					select {
					case <-release:
						return io.EOF
					case <-r.Context().Done():
						return r.Context().Err()
					}
				}
				if !headersSent {
					return nil, wait()
				}
				header := []byte{'F', 'L', 'V', 1, 5, 0, 0, 0, 9}
				return &http.Response{StatusCode: http.StatusOK, Body: stopTestBody{read: func(b []byte) (int, error) {
					if len(header) > 0 {
						n := copy(b, header)
						header = header[n:]
						return n, nil
					}
					return 0, wait()
				}}}, nil
			})
			u, err := url.Parse("https://example.com/stream.flv")
			require.NoError(t, err)
			done := make(chan error, 1)
			output := filepath.Join(t.TempDir(), "recording.flv")
			go func() { done <- p.ParseLiveStream(context.Background(), &live.StreamUrlInfo{Url: u}, nil, output) }()
			<-entered
			require.NoError(t, p.Stop())
			select {
			case err := <-done:
				require.NoError(t, err, "正常停止必须允许尾段继续登记")
				close(release)
			case <-time.After(time.Second):
				t.Error("Stop 没有解除阻塞的网络读取")
				close(release)
				<-done
			}
			if headersSent {
				content, err := os.ReadFile(output)
				require.NoError(t, err)
				require.Len(t, content, 9, "已写尾段不能被停止逻辑删除")
			}
		})
	}
}
