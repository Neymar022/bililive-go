package subtitle

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyntheticIdentityEvidenceReplay(t *testing.T) {
	data, err := os.ReadFile("testdata/library-recovery/synthetic-identity-evidence.json")
	require.NoError(t, err)
	var rows []struct {
		Path       string `json:"path"`
		Output     string `json:"output_path"`
		Start      string `json:"start_time"`
		SHA        string `json:"metadata_sha256"`
		TimeReason string `json:"time_reason"`
		Exact      bool   `json:"output_exact"`
	}
	require.NoError(t, json.Unmarshal(data, &rows))
	require.Len(t, rows, 132)
	accepted, blocked := 0, 0
	for _, row := range rows {
		t.Run(row.Path, func(t *testing.T) {
			_, err := CheckLibraryIdentityEvidence(row.Path, row.Output, row.Start)
			expected := row.TimeReason == "match" && row.Exact
			if expected {
				require.NoError(t, err)
				accepted++
			} else {
				require.Error(t, err)
				blocked++
			}
			t.Logf("source=record_meta.start_time metadata_sha=%s output_exact=%v result=%v", row.SHA, row.Exact, err)
		})
	}
	require.Equal(t, 87, accepted)
	require.Equal(t, 45, blocked)
}
