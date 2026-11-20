//go:build season_bootstrap_tool

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

func TestSeasonBootstrapA30Identity(t *testing.T) {
	t.Run("real_array_prepare", func(t *testing.T) {
		f := identityFixture(t)
		plan, err := prepare(context.Background(), f.proof, f.db, f.observer, f.season, "identity-fixture")
		require.NoError(t, err)
		require.Len(t, plan.Plan.LegacyEntries, 1)
		require.Equal(t, f.identity, plan.Plan.LegacyEntries[0].Identity)
		require.Equal(t, "101", plan.Plan.LegacyEntries[0].KnownIndexMapping.FileID)
		require.Empty(t, plan.Expected.BaselineLegacyUnconfirmed)
		require.NoFileExists(t, filepath.Join(f.season, subtitle.LibraryAssignmentsFilename))
	})
	for _, test := range []struct {
		name  string
		value any
	}{
		{"missing", []any{}},
		{"multiple", []any{map[string]any{"type": "bililive-recorded-at", "value": "1698637354560888"}, map[string]any{"type": "bililive-recorded-at", "value": "1698637354560888"}}},
		{"wrong_type", []any{map[string]any{"type": "other", "value": "1698637354560888"}}},
		{"empty", []any{map[string]any{"type": "bililive-recorded-at", "value": ""}}},
		{"invalid_decimal", []any{map[string]any{"type": "bililive-recorded-at", "value": "not-an-id"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := identityFixture(t)
			raw, err := os.ReadFile(f.proof)
			require.NoError(t, err)
			var proof map[string]any
			require.NoError(t, json.Unmarshal(raw, &proof))
			season := proof["seasons"].(map[string]any)[f.season].(map[string]any)
			season["entries"].([]any)[0].(map[string]any)["recorded_identity"] = test.value
			writeIdentityJSON(t, f.proof, proof)
			_, err = prepare(context.Background(), f.proof, f.db, f.observer, f.season, "identity-fixture")
			require.ErrorContains(t, err, "a30_recorded_identity_invalid")
			require.NoFileExists(t, filepath.Join(f.season, subtitle.LibraryAssignmentsFilename))
		})
	}
}

type identityFixtureState struct {
	proof, db, observer, season string
	identity                    int64
}

func identityFixture(t *testing.T) identityFixtureState {
	t.Helper()
	raw, err := os.ReadFile("testdata/a30-identity-shape.json")
	require.NoError(t, err)
	var slice struct {
		Seasons map[string]struct {
			Entries []struct {
				Identity json.RawMessage `json:"recorded_identity"`
			} `json:"entries"`
		} `json:"seasons"`
	}
	require.NoError(t, json.Unmarshal(raw, &slice))
	identityRaw := slice.Seasons["fixture-season"].Entries[0].Identity
	var ids []struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(identityRaw, &ids))
	require.Len(t, ids, 1)
	identity, err := strconv.ParseInt(ids[0].Value, 10, 64)
	require.NoError(t, err)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	library := filepath.Join(root, "video")
	season := filepath.Join(library, "Host", "Season 01")
	require.NoError(t, os.MkdirAll(season, 0755))
	at := time.UnixMicro(identity/8 + 1577836800*1000000)
	date := at.In(time.FixedZone("UTC8", 8*3600)).Format("2006-01-02")
	media := filepath.Join(season, fmt.Sprintf("Host.S01E%d.%s - fixture.mp4", identity, date))
	nfo, err := subtitle.BuildLibraryEpisodeNFO(media, at, "fixture")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(media, []byte("media"), 0600))
	require.NoError(t, os.WriteFile(strings.TrimSuffix(media, ".mp4")+".nfo", []byte(nfo), 0600))
	stat, err := subtitle.AssignmentStat(media)
	require.NoError(t, err)
	row := map[string]any{"path": media, "file_id": 101, "file_size": stat.Size, "category_id": "category", "library_id": 4, "season": 1, "episode": 1, "ordinal_file_count": 1, "audio_count": 1, "video_count": 1, "episodes": []map[string]any{{"id": 201, "category_id": "category", "library_id": 4, "season": 1, "episode": 1, "paths": []string{media}}}, "shows": []map[string]any{{"id": 301, "name": "Host", "category_id": "category", "library_id": 4}}}
	proof := filepath.Join(root, "proof.json")
	writeIdentityJSON(t, proof, map[string]any{"seasons": map[string]any{season: map[string]any{"entries": []map[string]any{{"media_path": media, "episode": "1", "recorded_identity": identityRaw, "media": map[string]any{"exists": true, "size": stat.Size, "dev": stat.Device, "inode": stat.Inode, "mtime_ns": stat.MtimeNS}, "observer_exact": []any{row}}, {"media_path": filepath.Join(season, "orphan.nfo"), "recorded_identity": []map[string]any{{"type": "unrelated-orphan", "value": ""}, {"type": "unrelated-orphan", "value": ""}}, "media": map[string]any{"exists": false}}}}}})
	db := filepath.Join(root, "pipeline.db")
	handle, err := sql.Open("sqlite", db)
	require.NoError(t, err)
	_, err = handle.Exec(`CREATE TABLE pipeline_library_recoveries(operation_id TEXT,kind TEXT,season TEXT,active INTEGER,record_json TEXT)`)
	require.NoError(t, err)
	require.NoError(t, handle.Close())
	observer := filepath.Join(root, "snapshot.json")
	now := time.Now().UTC()
	writeIdentityJSON(t, observer, map[string]any{"schema_version": 2, "adapter_version": "ugreen-fixed-select-v2", "generated_at": now, "library_root": library, "platform": map[string]any{"state": "confirmed", "pid": 123, "uid": 0, "start_ticks": 1, "started_at": now.Add(-time.Hour), "observed_at": now, "startup_path": subtitle.LibraryPlatformStartupPath, "build_path": subtitle.LibraryPlatformBuildPath, "build_sha256": strings.Repeat("b", 64)}, "libraries": []map[string]any{{"id": 4, "roots": []string{library}, "read_local_info_first": true, "nfo_info_save_to_local": false}}, "files": []any{row}})
	return identityFixtureState{proof: proof, db: db, observer: observer, season: season, identity: identity}
}
func writeIdentityJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
}
