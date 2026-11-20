package pipeline

import (
	"context"
	"encoding/json"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func snapshotFixture(t *testing.T) (*LibraryIndexSnapshotObserver, LibraryIndexExpected, map[string]any) {
	t.Helper()
	root := t.TempDir()
	season := filepath.Join(root, "主播", "Season 01")
	if err := os.MkdirAll(season, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(season, "主播.S01E12345.2026-01-01 - 标题.mp4")
	if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path[:len(path)-4]+".nfo", []byte(`<episodedetails><season>1</season><episode>1</episode><uniqueid type="bililive-recorded-at">12345</uniqueid></episodedetails>`), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	data := map[string]any{
		"schema_version": 2, "adapter_version": "ugreen-fixed-select-v2", "platform": subtitle.LibraryRecoveryPlatform{State: "confirmed", PID: 123, UID: 0, StartTicks: 10, StartedAt: now.Add(-time.Hour), ObservedAt: now, StartupPath: subtitle.LibraryPlatformStartupPath, BuildPath: subtitle.LibraryPlatformBuildPath, BuildSHA256: strings.Repeat("b", 64)}, "generated_at": now, "library_root": root,
		"libraries": []any{map[string]any{"id": 4, "roots": []string{root}, "read_local_info_first": true, "nfo_info_save_to_local": false}},
		"files": []any{map[string]any{"path": path, "file_id": 10, "file_size": 5, "category_id": "category", "library_id": 4, "season": 1, "episode": 1, "ordinal_file_count": 1, "audio_count": 1, "video_count": 1,
			"episodes": []any{map[string]any{"id": 20, "category_id": "category", "library_id": 4, "season": 1, "episode": 1, "paths": []string{path}}},
			"shows":    []any{map[string]any{"id": 30, "category_id": "category", "library_id": 4, "name": "主播 "}}}},
	}
	o := &LibraryIndexSnapshotObserver{path: filepath.Join(root, "snapshot.json"), now: func() time.Time { return now }}
	e := LibraryIndexExpected{OperationID: "operation", LibraryRoot: root, Show: "主播", Season: season, Path: path, Identity: "12345", Ordinal: 1, MediaSize: 5, PublishedAt: now.Add(-time.Second)}
	return o, e, data
}

func writeSnapshot(t *testing.T, o *LibraryIndexSnapshotObserver, data map[string]any) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(o.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLibraryIndexSnapshotRoundTrip(t *testing.T) {
	o, e, data := snapshotFixture(t)
	writeSnapshot(t, o, data)
	before := e
	before.Path = ""
	before.PublishedAt = time.Time{}
	r, err := o.VerifyPublication(context.Background(), before)
	if err != nil || r.State != "verified" || len(r.Mappings) != 1 {
		t.Fatalf("baseline: %+v %v", r, err)
	}
	e.Protected = r.Mappings
	r, err = o.VerifyPublication(context.Background(), e)
	if err != nil || r.State != "verified" {
		t.Fatalf("after: %+v %v", r, err)
	}
	data["files"].([]any)[0].(map[string]any)["file_id"] = 11
	writeSnapshot(t, o, data)
	r, err = o.VerifyPublication(context.Background(), e)
	if err != nil || r.State != "external_index_conflict" || r.Reason != "protected_mapping_changed" {
		t.Fatalf("identity loss: %+v %v", r, err)
	}
}

func TestLibraryIndexSnapshotRejectsIncompleteOrConflictingEvidence(t *testing.T) {
	tests := []struct {
		name, want string
		change     func(*LibraryIndexSnapshotObserver, *LibraryIndexExpected, map[string]any)
	}{
		{"v1_snapshot", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			d["schema_version"] = 1
			d["adapter_version"] = "ugreen-fixed-select-v1"
		}},
		{"missing_platform", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			delete(d, "platform")
		}},
		{"platform_unconfirmed", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			p := d["platform"].(subtitle.LibraryRecoveryPlatform)
			p.State = "platform_unconfirmed"
			d["platform"] = p
		}},
		{"platform_stale", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			p := d["platform"].(subtitle.LibraryRecoveryPlatform)
			p.ObservedAt = o.now().Add(-2 * time.Minute)
			d["platform"] = p
		}},

		{"stale", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			d["generated_at"] = o.now().Add(-2 * time.Minute)
		}},
		{"future", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			d["generated_at"] = o.now().Add(time.Minute)
		}},
		{"before_publish", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			e.PublishedAt = o.now()
		}},
		{"writeback", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			d["libraries"].([]any)[0].(map[string]any)["nfo_info_save_to_local"] = true
		}},
		{"setting_missing", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			delete(d["libraries"].([]any)[0].(map[string]any), "nfo_info_save_to_local")
		}},
		{"missing_index", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) { d["files"] = []any{} }},
		{"duplicate", "external_index_conflict", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			d["files"] = append(d["files"].([]any), d["files"].([]any)[0])
		}},
		{"size_lag", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			d["files"].([]any)[0].(map[string]any)["file_size"] = 4
		}},
		{"wrong_show", "external_index_conflict", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			d["files"].([]any)[0].(map[string]any)["shows"].([]any)[0].(map[string]any)["name"] = "另一个"
		}},
		{"wrong_ordinal", "external_index_conflict", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) { e.Ordinal = 2 }},
		{"wrong_identity", "external_index_conflict", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) { e.Identity = "other" }},
		{"root_escape", "index_unconfirmed", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			e.Season = filepath.Join(e.LibraryRoot, "..", "elsewhere", "Season 01")
		}},
		{"missing_protected", "external_index_conflict", func(o *LibraryIndexSnapshotObserver, e *LibraryIndexExpected, d map[string]any) {
			e.Protected = []LibraryIndexMapping{{Path: e.Path}}
			d["files"] = []any{}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, e, d := snapshotFixture(t)
			tc.change(o, &e, d)
			writeSnapshot(t, o, d)
			r, err := o.VerifyPublication(context.Background(), e)
			if err != nil || r.State != tc.want {
				t.Fatalf("%+v %v", r, err)
			}
		})
	}
}

func TestLibraryIndexSnapshotUnavailableAndCancellation(t *testing.T) {
	o, e, _ := snapshotFixture(t)
	r, err := o.VerifyPublication(context.Background(), e)
	if err != nil || r.State != "index_unconfirmed" {
		t.Fatalf("%+v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = o.VerifyPublication(ctx, e); err != context.Canceled {
		t.Fatalf("%v", err)
	}
}
