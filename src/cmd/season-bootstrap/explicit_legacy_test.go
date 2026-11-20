//go:build season_bootstrap_tool

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
	"github.com/stretchr/testify/require"
)

func TestExplicitLegacyAdmission(t *testing.T) {
	t.Run("prepare_apply_reopen_runtime_and_protection", func(t *testing.T) {
		f := newExplicitFixture(t)
		raw, err := f.plan(t)
		require.NoError(t, err)
		var plan bootstrapPlan
		require.NoError(t, json.Unmarshal(raw, &plan))
		require.Len(t, plan.Expected.Protected, 1)
		require.Len(t, plan.Expected.BaselineLegacyUnconfirmed, 1)
		require.EqualValues(t, 2, plan.Expected.BaselineLegacyUnconfirmed[0].Ordinal)
		require.NoError(t, os.WriteFile(f.planPath, raw, 0600))
		require.NoError(t, f.apply(t))
		ledger, err := subtitle.LoadLibraryAssignments(f.season)
		require.NoError(t, err)
		require.Equal(t, "legacy_unconfirmed", ledger.LegacyEntries[1].IndexState)
		require.Nil(t, ledger.LegacyEntries[1].KnownIndexMapping)
		encoded, err := json.Marshal(ledger)
		require.NoError(t, err)
		require.Contains(t, string(encoded), "legacy_admission")
		observer := pipeline.NewLibraryIndexSnapshotObserver(f.observer)
		expected := pipeline.LibraryIndexExpected{Scope: "append_by_admission", LibraryRoot: filepath.Dir(filepath.Dir(f.season)), Season: f.season, Show: "Host"}
		observed, err := observer.VerifyPublication(context.Background(), expected)
		require.NoError(t, err)
		require.Equal(t, "verified", observed.State)
		require.Len(t, observed.LegacyUnconfirmed, 1)
		target, assignment := f.publishTarget(t)
		f.snapshot(t, []any{f.row1})
		stat, err := subtitle.AssignmentStat(target)
		require.NoError(t, err)
		expected.Path = target
		expected.Identity = strconv.FormatInt(assignment.Identity, 10)
		expected.Ordinal = assignment.Ordinal
		expected.MediaSize = stat.Size
		expected.AssignmentOperation = assignment.OperationID
		observed, err = observer.VerifyPublication(context.Background(), expected)
		require.NoError(t, err)
		require.Equal(t, "index_unconfirmed", observed.State)
		require.Len(t, observed.LegacyUnconfirmed, 1)
		require.Len(t, observed.OwnedIndexPending, 1)
		f.snapshot(t, []any{f.row1, f.row2})
		observed, err = observer.VerifyPublication(context.Background(), expected)
		require.NoError(t, err)
		require.Equal(t, "index_unconfirmed", observed.State)
		require.Empty(t, observed.LegacyUnconfirmed)
		lease, err := subtitle.LockLibraryAssignments(context.Background(), f.season)
		require.NoError(t, err)
		var mappings []subtitle.AssignmentIndexMapping
		for _, m := range observed.Mappings {
			mappings = append(mappings, subtitle.AssignmentIndexMapping{Path: m.Path, FileID: m.FileID, EpisodeID: m.EpisodeID, ShowID: m.ShowID, SeasonID: m.SeasonID, CategoryID: m.CategoryID, LibraryID: m.LibraryID, Ordinal: m.Ordinal, SeasonNumber: m.SeasonNumber})
		}
		require.NoError(t, lease.MergeIndex(mappings))
		lease.Close()
		ledger, err = subtitle.LoadLibraryAssignments(f.season)
		require.NoError(t, err)
		require.Equal(t, "protected", ledger.LegacyEntries[1].IndexState)
		require.Equal(t, "102", ledger.LegacyEntries[1].KnownIndexMapping.FileID)
		f.snapshot(t, []any{f.row1, f.row2, f.row2})
		observed, err = observer.VerifyPublication(context.Background(), expected)
		require.NoError(t, err)
		require.Equal(t, "external_index_conflict", observed.State)
		changed := cloneExplicit(f.row1)
		changed["file_id"] = 999
		f.snapshot(t, []any{changed, f.row2})
		observed, err = observer.VerifyPublication(context.Background(), expected)
		require.NoError(t, err)
		require.Equal(t, "external_index_conflict", observed.State)
	})
	for _, mode := range []string{"unapproved", "wrong_identity", "wrong_stat", "duplicate", "cross_season", "unknown_schema", "empty_reviewer", "bad_timestamp", "unsupported_reason", "not_in_baseline", "baseline_regular_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			f := newExplicitFixture(t)
			approval := readExplicit(t, f.approval)
			entry := approval["entries"].([]any)[0].(map[string]any)
			switch mode {
			case "unapproved":
				proof := readExplicit(t, f.proof)
				first := proof["seasons"].(map[string]any)[f.season].(map[string]any)["entries"].([]any)[0].(map[string]any)
				media := cloneExplicit(first["media"].(map[string]any))
				media["regular"] = true
				id := first["recorded_identity"].([]any)[0].(map[string]any)["value"]
				approval["entries"] = []any{map[string]any{"path": first["media_path"], "identity": id, "ordinal": 1, "media": media, "reason": "existing_media_index_absent"}}
			case "wrong_identity":
				entry["identity"] = strconv.FormatInt(f.id2+8, 10)
			case "wrong_stat":
				entry["media"].(map[string]any)["size"] = 999
			case "duplicate":
				approval["entries"] = []any{entry, entry}
			case "cross_season":
				entry["path"] = filepath.Join(filepath.Dir(f.season), "Season 02", filepath.Base(f.media2))
			case "unknown_schema":
				approval["schema"] = 2
			case "empty_reviewer":
				approval["approved_by"] = " "
			case "bad_timestamp":
				approval["approved_at"] = "not-a-time"
			case "unsupported_reason":
				entry["reason"] = "ignore_everything"
			case "not_in_baseline":
				entry["path"] = filepath.Join(f.season, "unlisted.mp4")
			case "baseline_regular_mismatch":
				proof := readExplicit(t, f.proof)
				entries := proof["seasons"].(map[string]any)[f.season].(map[string]any)["entries"].([]any)
				entries[len(entries)-1].(map[string]any)["media"].(map[string]any)["regular"] = false
				writeIdentityJSON(t, f.proof, proof)
			}
			writeIdentityJSON(t, f.approval, approval)
			_, err := f.plan(t)
			require.Error(t, err)
			require.NoFileExists(t, filepath.Join(f.season, subtitle.LibraryAssignmentsFilename))
		})
	}
	for _, mode := range []string{"approval_content_changed", "plan_path_changed", "apply_without_explicit_path"} {
		t.Run(mode, func(t *testing.T) {
			f := newExplicitFixture(t)
			raw, err := f.plan(t)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(f.planPath, raw, 0600))
			if mode == "approval_content_changed" {
				approval := readExplicit(t, f.approval)
				approval["evidence_ref"] = "changed-review-evidence"
				writeIdentityJSON(t, f.approval, approval)
			}
			if mode == "plan_path_changed" {
				replacement := filepath.Join(filepath.Dir(f.approval), "other-approval.json")
				original, e := os.ReadFile(f.approval)
				require.NoError(t, e)
				require.NoError(t, os.WriteFile(replacement, original, 0600))
				plan := readExplicit(t, f.planPath)
				plan["legacy_approval_path"] = replacement
				plan["plan"].(map[string]any)["legacy_admission"].(map[string]any)["approval_path"] = replacement
				writeIdentityJSON(t, f.planPath, plan)
			}
			if mode == "apply_without_explicit_path" {
				_, err = captureExplicit(t, []string{"apply", "--plan", f.planPath, "--operation", "fixture-explicit", "--maintenance-proof", f.maintenance})
			} else {
				err = f.apply(t)
			}
			require.ErrorContains(t, err, "legacy approval")
			require.NoFileExists(t, filepath.Join(f.season, subtitle.LibraryAssignmentsFilename))
		})
	}
	t.Run("runtime_rejects_missing_or_tampered_durable_approval", func(t *testing.T) {
		f := newExplicitFixture(t)
		raw, err := f.plan(t)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(f.planPath, raw, 0600))
		require.NoError(t, f.apply(t))
		path := filepath.Join(f.season, subtitle.LibraryAssignmentsFilename)
		original, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, mode := range []string{"missing", "tampered_stat"} {
			require.NoError(t, os.WriteFile(path, original, 0600))
			ledger := readExplicit(t, path)
			if mode == "missing" {
				delete(ledger, "legacy_admission")
			} else {
				ledger["legacy_admission"].(map[string]any)["approval"].(map[string]any)["entries"].([]any)[0].(map[string]any)["media"].(map[string]any)["size"] = 999
			}
			writeIdentityJSON(t, path, ledger)
			observed, err := pipeline.NewLibraryIndexSnapshotObserver(f.observer).VerifyPublication(context.Background(), pipeline.LibraryIndexExpected{Scope: "append_by_admission", LibraryRoot: filepath.Dir(filepath.Dir(f.season)), Season: f.season, Show: "Host"})
			require.NoError(t, err)
			require.NotEqual(t, "verified", observed.State)
			require.NotEqual(t, "target_verified", observed.State)
		}
	})
}

type explicitFixture struct {
	identityFixtureState
	approval, planPath, maintenance, media2 string
	id2                                     int64
	at                                      time.Time
	row1, row2                              map[string]any
}

func newExplicitFixture(t *testing.T) explicitFixture {
	t.Helper()
	base := identityFixture(t)
	root := filepath.Dir(base.proof)
	f := explicitFixture{identityFixtureState: base, approval: filepath.Join(root, "approval.json"), planPath: filepath.Join(root, "plan.json"), maintenance: filepath.Join(root, "maintenance.json")}
	f.at = time.UnixMicro(base.identity/8 + 1577836800*1000000).Add(24 * time.Hour)
	f.id2 = (f.at.UnixMicro() - 1577836800*1000000) * 8
	f.media2 = filepath.Join(f.season, "Host.S01E"+strconv.FormatInt(f.id2, 10)+"."+f.at.In(time.FixedZone("UTC8", 8*3600)).Format("2006-01-02")+" - second.mp4")
	nfo, err := subtitle.BuildLibraryEpisodeNFO(f.media2, f.at, "fixture")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.media2, []byte("media"), 0600))
	require.NoError(t, os.WriteFile(strings.TrimSuffix(f.media2, ".mp4")+".nfo", []byte(nfo), 0600))
	stat, err := subtitle.AssignmentStat(f.media2)
	require.NoError(t, err)
	proof := readExplicit(t, f.proof)
	season := proof["seasons"].(map[string]any)[f.season].(map[string]any)
	entries := season["entries"].([]any)
	entries = append(entries, map[string]any{"media_path": f.media2, "episode": "2", "recorded_identity": []map[string]any{{"type": "bililive-recorded-at", "value": strconv.FormatInt(f.id2, 10)}}, "media": map[string]any{"exists": true, "regular": true, "size": stat.Size, "dev": stat.Device, "inode": stat.Inode, "mtime_ns": stat.MtimeNS}, "observer_exact": []any{}})
	season["entries"] = entries
	writeIdentityJSON(t, f.proof, proof)
	snapshot := readExplicit(t, f.observer)
	f.row1 = snapshot["files"].([]any)[0].(map[string]any)
	f.row2 = cloneExplicit(f.row1)
	f.row2["path"] = f.media2
	f.row2["file_id"] = 102
	f.row2["episode"] = 2
	episode := f.row2["episodes"].([]any)[0].(map[string]any)
	episode["id"] = 202
	episode["episode"] = 2
	episode["paths"] = []string{f.media2}
	approval := map[string]any{"schema": 1, "season": f.season, "approved_by": "fixture-reviewer", "approved_at": time.Now().UTC().Add(-time.Minute), "evidence_ref": "fixture-evidence-v1", "entries": []any{map[string]any{"path": f.media2, "identity": strconv.FormatInt(f.id2, 10), "ordinal": 2, "media": map[string]any{"exists": true, "regular": true, "size": stat.Size, "dev": stat.Device, "inode": stat.Inode, "mtime_ns": stat.MtimeNS}, "reason": "existing_media_index_absent"}}}
	writeIdentityJSON(t, f.approval, approval)
	writeIdentityJSON(t, f.maintenance, maintenanceProof{VerifiedAt: time.Now().UTC(), AllWritersLedgerAware: true, NoSeasonWriter: true, Operator: "fixture-operator", DeploymentReference: "fixture-compatible-runtime"})
	return f
}
func (f explicitFixture) plan(t *testing.T) ([]byte, error) {
	t.Helper()
	return captureExplicit(t, []string{"plan", "--a30-evidence", f.proof, "--db", f.db, "--observer", f.observer, "--season", f.season, "--operation", "fixture-explicit", "--legacy-unconfirmed-approval", f.approval})
}
func (f explicitFixture) apply(t *testing.T) error {
	t.Helper()
	_, err := captureExplicit(t, []string{"apply", "--plan", f.planPath, "--operation", "fixture-explicit", "--maintenance-proof", f.maintenance, "--legacy-unconfirmed-approval", f.approval})
	return err
}
func captureExplicit(t *testing.T, args []string) ([]byte, error) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdout-*.json")
	require.NoError(t, err)
	previous := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = previous; file.Close() }()
	err = run(args)
	require.NoError(t, file.Sync())
	raw, e := os.ReadFile(file.Name())
	require.NoError(t, e)
	return raw, err
}
func readExplicit(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	require.NoError(t, decoder.Decode(&value))
	return value
}
func cloneExplicit(value map[string]any) map[string]any {
	raw, _ := json.Marshal(value)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var copy map[string]any
	_ = decoder.Decode(&copy)
	return copy
}
func (f explicitFixture) snapshot(t *testing.T, rows []any) {
	t.Helper()
	value := readExplicit(t, f.observer)
	now := time.Now().UTC()
	value["generated_at"] = now
	value["platform"].(map[string]any)["observed_at"] = now
	value["files"] = rows
	writeIdentityJSON(t, f.observer, value)
}
func (f explicitFixture) publishTarget(t *testing.T) (string, subtitle.LibraryAssignment) {
	t.Helper()
	at := f.at.Add(24 * time.Hour)
	id := (at.UnixMicro() - 1577836800*1000000) * 8
	target := filepath.Join(f.season, "Host.S01E"+strconv.FormatInt(id, 10)+"."+at.In(time.FixedZone("UTC8", 8*3600)).Format("2006-01-02")+" - new.mp4")
	assignment, _, err := subtitle.ReserveLibraryAssignment(context.Background(), f.season, "fixture-new-session", target, []int64{3})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(assignment.StagingRoot, 0700))
	nfo, err := subtitle.BuildLibraryEpisodeNFO(target, at, "fixture")
	require.NoError(t, err)
	var files []subtitle.AssignmentInstall
	for _, ext := range []string{".mp4", ".srt", ".ass", ".nfo", ".subtitle.json", ".jpg"} {
		path := filepath.Join(assignment.StagingRoot, "prepared"+ext)
		content := []byte("media")
		if ext == ".nfo" {
			content = []byte(nfo)
		}
		require.NoError(t, os.WriteFile(path, content, 0600))
		files = append(files, subtitle.AssignmentInstall{Staged: path, Target: strings.TrimSuffix(target, ".mp4") + ext})
	}
	lease, err := subtitle.LockLibraryAssignments(context.Background(), f.season)
	require.NoError(t, err)
	require.NoError(t, lease.Prepare(assignment.OperationID, lease.Ledger().Revision, files))
	require.NoError(t, lease.Finish(assignment.OperationID, lease.Ledger().Revision))
	lease.Close()
	return target, assignment
}
