package servers

import (
	"encoding/json"
	"errors"
	"github.com/bililive-go/bililive-go/src/pipeline"
	"io"
	"net/http"
)

var ErrWorkerSubmissionControlUnsupported = errors.New("worker_submission_control_unsupported_platform")

func makeWorkerSubmissionsUpdateHandler(manager *pipeline.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Accepting *bool `json:"accepting"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || body.Accepting == nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "expected accepting boolean", http.StatusBadRequest)
			return
		}
		if err := manager.SetWorkerSubmissions(*body.Accepting); err != nil {
			http.Error(w, "worker submission control unavailable", http.StatusServiceUnavailable)
			return
		}
		makeWorkerSubmissionsStatusHandler(manager)(w, r)
	}
}
