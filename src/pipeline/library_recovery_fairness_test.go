//go:build linux

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bililive-go/bililive-go/src/subtitle"
)

type standardsObserver struct {
	calls       map[string]int
	secondState string
	firstError  bool
}

func (o *standardsObserver) VerifyPublication(_ context.Context, e LibraryIndexExpected) (LibraryIndexObservation, error) {
	o.calls[e.OperationID]++
	if e.OperationID == "a" {
		if o.firstError {
			return LibraryIndexObservation{}, errors.New("observer temporarily unavailable")
		}
		return LibraryIndexObservation{State: "index_unconfirmed", Reason: "media_not_indexed"}, nil
	}
	return LibraryIndexObservation{State: o.secondState, Reason: "independent-season-result"}, nil
}

// 验收断言：第一季暂缺索引时，另一季仍应得到复查与冲突冻结。
func TestStandardsIndexRefreshDoesNotStarveOtherSeasons(t *testing.T) {
	for _, firstError := range []bool{false, true} {
		for _, want := range []string{"verified", "external_index_conflict"} {
			t.Run(fmt.Sprintf("%s/error=%t", want, firstError), func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				s, err := NewSQLiteStore(filepath.Join(root, "pipeline.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				observer := &standardsObserver{calls: map[string]int{}, secondState: want, firstError: firstError}
				m := NewManager(ctx, s, nil, nil)
				m.SetLibraryIndexObserver(observer)
				for _, id := range []string{"a", "b"} {
					season := filepath.Join(root, "video", id, "Season 01")
					if err := os.MkdirAll(season, 0700); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(season, "media.mp4")
					if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
						t.Fatal(err)
					}
					state, err := subtitle.CaptureLibraryFileState(path, false)
					if err != nil {
						t.Fatal(err)
					}
					r := &LibraryRecoveryRecord{OperationID: id, Kind: "files", Season: season, State: "index_unconfirmed", BeforeCaptured: true, Expected: LibraryIndexExpected{OperationID: id, Path: path, Season: season, MediaRevision: fmt.Sprintf("%d:%d:%d:%d", state.Device, state.Inode, state.Size, state.MtimeNS)}}
					if err := s.saveLibraryRecovery(ctx, r); err != nil {
						t.Fatal(err)
					}
				}
				for i := 0; i < 3; i++ {
					before := observer.calls["a"] + observer.calls["b"]
					m.refreshLibraryIndex(ctx, s)
					if observer.calls["a"]+observer.calls["b"]-before > 1 {
						t.Fatal("一轮不得超过一次observer调用")
					}
				}
				records, err := s.libraryRecoveries(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("3 polls: calls=%v, secondState=%s", observer.calls, records[1].State)
				if observer.calls["b"] == 0 || records[1].State != want {
					t.Fatalf("后续独立Season未被复查：want %s, got %s", want, records[1].State)
				}
			})
		}
	}
}
