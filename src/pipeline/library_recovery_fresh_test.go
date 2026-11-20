//go:build linux

package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

type freshRecoveryObserver struct {
	calls int
	mode  string
	seen  []LibraryIndexMapping
}

func (o *freshRecoveryObserver) VerifyPublication(ctx context.Context, e LibraryIndexExpected) (LibraryIndexObservation, error) {
	o.calls++
	o.seen = append([]LibraryIndexMapping(nil), e.Protected...)
	result, err := (fixtureIndexObserver{}).VerifyPublication(ctx, e)
	result.Mappings = []LibraryIndexMapping{{Path: e.Season + "/old.mp4", FileID: "11", EpisodeID: "12", ShowID: "13", Ordinal: 1}}
	switch o.mode {
	case "stale":
		result.State = "index_unconfirmed"
		result.Reason = "snapshot_stale"
	case "error":
		return result, errors.New("observer unavailable")
	case "build":
		result.Platform.BuildSHA256 = strings.Repeat("c", 64)
	case "platform":
		result.Platform.State = "platform_unconfirmed"
	case "conflict":
		result.Mappings[0].FileID = "99"
	}
	return result, err
}

func TestLibraryRecoveryHealthyRetryRequiresFreshBaseline(t *testing.T) {
	for _, mode := range []string{"stale", "error", "build", "platform", "conflict", "missing", "restart_missing", "restart_fresh"} {
		t.Run(mode, func(t *testing.T) {
			_, db, req := recoveryLinuxFixture(t)
			plan, err := subtitle.PlanLibraryIdentityRecovery(filepath.Dir(req.Path))
			require.NoError(t, err)
			for _, member := range plan.Members {
				require.NoError(t, os.WriteFile(member.NFO.Path, member.After, 0700))
			}
			s, err := NewSQLiteStore(db)
			require.NoError(t, err)
			defer func() { s.Close() }()
			record := &LibraryRecoveryRecord{OperationID: "task-fresh", Kind: "task", TaskID: req.TaskID, Season: filepath.Dir(req.Path), State: "resumed", Expected: LibraryIndexExpected{LibraryRoot: req.LibraryRoot, Show: "Host", Season: filepath.Dir(req.Path)}}
			require.NoError(t, s.saveLibraryRecovery(context.Background(), record))
			m := NewManager(context.Background(), s, nil, nil)
			obs := &freshRecoveryObserver{}
			m.SetLibraryIndexObserver(obs)
			require.NoError(t, m.EnsureLibraryPublication(context.Background(), req))
			require.Equal(t, 1, obs.calls)
			records, err := s.libraryRecoveries(context.Background())
			require.NoError(t, err)
			protected := append([]LibraryIndexMapping(nil), records[0].Expected.Protected...)
			require.Len(t, protected, 1)
			obs.mode = mode
			if strings.HasPrefix(mode, "restart_") {
				require.NoError(t, s.Close())
				s, err = NewSQLiteStore(db)
				require.NoError(t, err)
				m = NewManager(context.Background(), s, nil, nil)
			}
			if mode == "missing" {
				m.SetLibraryIndexObserver(nil)
			}
			if mode == "restart_fresh" {
				obs.mode = ""
				m.SetLibraryIndexObserver(obs)
			}
			allowed := 0
			if err = m.EnsureLibraryPublication(context.Background(), req); err == nil {
				allowed++
			}
			if mode == "restart_fresh" {
				require.Equal(t, 1, allowed)
			} else {
				require.Zero(t, allowed, "失去新鲜证明后不得放行正式恢复发布")
			}
			records, err = s.libraryRecoveries(context.Background())
			require.NoError(t, err)
			require.Equal(t, protected, records[0].Expected.Protected, "不得覆盖原保护映射")
			if mode != "missing" && mode != "restart_missing" {
				require.Equal(t, 2, obs.calls)
				require.Equal(t, protected, obs.seen)
			}
			_, err = os.Stat(req.Path)
			require.True(t, os.IsNotExist(err), "此门禁测试不产生发布文件")
		})
	}
}

func TestLibraryRecoveryOrdinaryHealthyPublicationKeepsExistingBehavior(t *testing.T) {
	_, db, req := recoveryLinuxFixture(t)
	plan, err := subtitle.PlanLibraryIdentityRecovery(filepath.Dir(req.Path))
	require.NoError(t, err)
	for _, member := range plan.Members {
		require.NoError(t, os.WriteFile(member.NFO.Path, member.After, 0700))
	}
	s, err := NewSQLiteStore(db)
	require.NoError(t, err)
	defer s.Close()
	m := NewManager(context.Background(), s, nil, nil)
	require.NoError(t, m.EnsureLibraryPublication(context.Background(), req))
	records, err := s.libraryRecoveries(context.Background())
	require.NoError(t, err)
	require.Empty(t, records)
}
