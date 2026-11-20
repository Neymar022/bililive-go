//go:build season_bootstrap_tool

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bililive-go/bililive-go/src/pipeline"
	"github.com/bililive-go/bililive-go/src/subtitle"
)

type baselineEntry struct {
	MediaPath string                     `json:"media_path"`
	Episode   string                     `json:"episode"`
	Identity  []baselineRecordedIdentity `json:"recorded_identity"`
	Media     struct {
		Exists  bool   `json:"exists"`
		Regular bool   `json:"regular"`
		Size    int64  `json:"size"`
		Device  uint64 `json:"dev"`
		Inode   uint64 `json:"inode"`
		Mtime   int64  `json:"mtime_ns"`
	} `json:"media"`
	Observer []struct {
		FileID   int64  `json:"file_id"`
		Ordinal  int64  `json:"episode"`
		Season   int    `json:"season"`
		Category string `json:"category_id"`
		Library  int64  `json:"library_id"`
		Shows    []struct {
			ID int64 `json:"id"`
		} `json:"shows"`
		Episodes []struct {
			ID int64 `json:"id"`
		} `json:"episodes"`
	} `json:"observer_exact"`
}

type baselineRecordedIdentity struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func (e baselineEntry) recordedIdentity() (string, error) {
	if !e.Media.Exists {
		return "", nil
	}
	if len(e.Identity) != 1 || e.Identity[0].Type != "bililive-recorded-at" {
		return "", errors.New("a30_recorded_identity_invalid: existing media requires one recorded-at identity")
	}
	value := e.Identity[0].Value
	// 与既有媒体身份的安全整数范围相同；不接受空白、符号或非规范十进制。
	number, err := strconv.ParseUint(value, 10, 53)
	if err != nil || number == 0 || strconv.FormatUint(number, 10) != value {
		return "", errors.New("a30_recorded_identity_invalid: positive canonical identity required")
	}
	return value, nil
}

type baseline struct {
	Seasons map[string]struct {
		Entries   []baselineEntry `json:"entries"`
		Truncated bool            `json:"truncated"`
	} `json:"seasons"`
}
type bootstrapPlan struct {
	Schema              int                                      `json:"schema"`
	A30Path             string                                   `json:"a30_path"`
	DBPath              string                                   `json:"db_path"`
	ObserverPath        string                                   `json:"observer_path"`
	Expected            pipeline.LibraryIndexExpected            `json:"expected"`
	Plan                *subtitle.LibraryAssignmentBootstrapPlan `json:"plan"`
	LegacyApprovalPath  string                                   `json:"legacy_approval_path,omitempty"`
	LegacyApprovalBytes []byte                                   `json:"legacy_approval_bytes,omitempty"`
}
type maintenanceProof struct {
	VerifiedAt            time.Time `json:"verified_at"`
	AllWritersLedgerAware bool      `json:"all_writers_ledger_aware"`
	NoSeasonWriter        bool      `json:"no_season_writer"`
	Operator              string    `json:"operator"`
	DeploymentReference   string    `json:"deployment_reference"`
}

func readJSON(path string, v any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return errors.New("small regular private JSON required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func readLegacyAdmission(path string) (*subtitle.LibraryLegacyAdmission, []byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, nil, errors.New("absolute clean legacy approval path required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return nil, nil, errors.New("small regular legacy approval required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, nil, errors.New("legacy approval changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 32<<20+1))
	if err != nil || len(raw) > 32<<20 {
		return nil, nil, errors.New("legacy approval unreadable")
	}
	after, err := file.Stat()
	if err != nil || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, nil, errors.New("legacy approval changed while reading")
	}
	var approval subtitle.LibraryLegacyApproval
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&approval); err != nil {
		return nil, nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, nil, errors.New("legacy approval trailing data")
	}
	return &subtitle.LibraryLegacyAdmission{ApprovalPath: path, Approval: approval}, raw, nil
}
func sameLegacyAdmission(a, b *subtitle.LibraryLegacyAdmission) bool {
	left, e := json.Marshal(a)
	right, f := json.Marshal(b)
	return e == nil && f == nil && bytes.Equal(left, right)
}
func checkJournals(dbPath, season string) error {
	info, err := os.Lstat(dbPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("existing DB required")
	}
	u := url.URL{Scheme: "file", Path: dbPath}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA query_only=ON"); err != nil {
		return err
	}
	if _, err = db.Exec("BEGIN"); err != nil {
		return err
	}
	defer db.Exec("ROLLBACK")
	rows, err := db.Query("SELECT operation_id,kind,active,record_json FROM pipeline_library_recoveries WHERE season=?", season)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var operation, kind, raw string
		var active bool
		if err = rows.Scan(&operation, &kind, &active, &raw); err != nil {
			return err
		}
		var record pipeline.LibraryRecoveryRecord
		if json.Unmarshal([]byte(raw), &record) != nil {
			return errors.New("journal_invalid protects bootstrap season")
		}
		if record.OperationID != operation || record.Kind != kind {
			return errors.New("journal columns disagree")
		}
		if kind == "files" && active {
			return errors.New("active files recovery blocks bootstrap")
		}
	}
	return rows.Err()
}
func prepare(ctx context.Context, a30, db, observer, season, operation string, approvalPaths ...string) (*bootstrapPlan, error) {
	var proof baseline
	if err := readJSON(a30, &proof); err != nil {
		return nil, err
	}
	entry, ok := proof.Seasons[season]
	if !ok || entry.Truncated {
		return nil, errors.New("exact season absent or truncated in A30 evidence")
	}
	if err := checkJournals(db, season); err != nil {
		return nil, err
	}
	if len(approvalPaths) > 1 {
		return nil, errors.New("one legacy approval input required")
	}
	var admission *subtitle.LibraryLegacyAdmission
	var approvalBytes []byte
	var approvalPath string
	if len(approvalPaths) == 1 && approvalPaths[0] != "" {
		approvalPath = approvalPaths[0]
		var err error
		admission, approvalBytes, err = readLegacyAdmission(approvalPath)
		if err != nil {
			return nil, err
		}
	}
	var bindings []subtitle.LibraryLegacyAssignment
	for _, e := range entry.Entries {
		if !e.Media.Exists {
			continue
		}
		identity, err := e.recordedIdentity()
		if err != nil {
			return nil, err
		}
		id, err := strconv.ParseInt(identity, 10, 64)
		if err != nil {
			return nil, err
		}
		ordinal, err := strconv.ParseInt(e.Episode, 10, 64)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, subtitle.LibraryLegacyAssignment{Path: e.MediaPath, Identity: id, Ordinal: ordinal})
	}
	approved, err := subtitle.ValidateLibraryLegacyAdmission(season, admission, bindings)
	if err != nil {
		return nil, err
	}
	baselineByPath := map[string]baselineEntry{}
	for _, item := range entry.Entries {
		baselineByPath[item.MediaPath] = item
	}
	for path, approvedEntry := range approved {
		before, exists := baselineByPath[path]
		m := approvedEntry.Media
		if !exists || !before.Media.Exists || !before.Media.Regular || before.Media.Size != m.Size || before.Media.Device != m.Device || before.Media.Inode != m.Inode || before.Media.Mtime != m.MtimeNS {
			return nil, errors.New("legacy_approval_invalid: baseline media differs")
		}
	}
	expected := pipeline.LibraryIndexExpected{Scope: "append_bootstrap", OperationID: operation, LibraryRoot: filepath.Dir(filepath.Dir(season)), Season: season, Show: filepath.Base(filepath.Dir(season))}
	expected.LegacyAdmission = admission
	var known []subtitle.AssignmentIndexMapping
	var missing []string
	identities := map[string]string{}
	count := 0
	for _, e := range entry.Entries {
		if !e.Media.Exists {
			continue
		}
		identity, err := e.recordedIdentity()
		if err != nil {
			return nil, err
		}
		identities[e.MediaPath] = identity
		count++
		stat, err := subtitle.AssignmentStat(e.MediaPath)
		if err != nil {
			return nil, err
		}
		if stat.Device != e.Media.Device || stat.Inode != e.Media.Inode || stat.Size != e.Media.Size || stat.MtimeNS != e.Media.Mtime {
			return nil, errors.New("A30 media original changed")
		}
		ordinal, err := strconv.ParseInt(e.Episode, 10, 64)
		if err != nil {
			return nil, err
		}
		if len(e.Observer) == 0 {
			_, explicit := approved[e.MediaPath]
			if !explicit {
				return nil, errors.New("unapproved legacy mapping missing")
			}
			missing = append(missing, e.MediaPath)
			expected.BaselineLegacyUnconfirmed = append(expected.BaselineLegacyUnconfirmed, pipeline.LibraryIndexUnconfirmed{Path: e.MediaPath, Identity: identity, Ordinal: ordinal})
			continue
		}
		if len(e.Observer) != 1 || len(e.Observer[0].Shows) != 1 || len(e.Observer[0].Episodes) != 1 {
			return nil, errors.New("A30 mapping not unique")
		}
		row := e.Observer[0]
		mapping := pipeline.LibraryIndexMapping{Path: e.MediaPath, FileID: strconv.FormatInt(row.FileID, 10), EpisodeID: strconv.FormatInt(row.Episodes[0].ID, 10), ShowID: strconv.FormatInt(row.Shows[0].ID, 10), CategoryID: row.Category, LibraryID: strconv.FormatInt(row.Library, 10), Ordinal: ordinal, SeasonNumber: row.Season}
		expected.Protected = append(expected.Protected, mapping)
		known = append(known, subtitle.AssignmentIndexMapping{Path: mapping.Path, FileID: mapping.FileID, EpisodeID: mapping.EpisodeID, ShowID: mapping.ShowID, CategoryID: mapping.CategoryID, LibraryID: mapping.LibraryID, Ordinal: mapping.Ordinal, SeasonNumber: mapping.SeasonNumber})
	}
	observation, err := pipeline.NewLibraryIndexSnapshotObserver(observer).VerifyPublication(ctx, expected)
	if err != nil {
		return nil, err
	}
	if observation.State != "bootstrap_verified" {
		return nil, fmt.Errorf("bootstrap index gate %s: %s", observation.State, observation.Reason)
	}
	// 本次新确认的旧unknown同样写入plan保护前像；apply再观测变化时不能重新豁免。
	known = nil
	confirmed := map[string]bool{}
	for _, m := range observation.Mappings {
		confirmed[m.Path] = true
		known = append(known, subtitle.AssignmentIndexMapping{Path: m.Path, FileID: m.FileID, EpisodeID: m.EpisodeID, ShowID: m.ShowID, SeasonID: m.SeasonID, CategoryID: m.CategoryID, LibraryID: m.LibraryID, Ordinal: m.Ordinal, SeasonNumber: m.SeasonNumber})
	}
	expected.Protected = observation.Mappings
	unconfirmed := missing[:0]
	for _, path := range missing {
		if !confirmed[path] {
			unconfirmed = append(unconfirmed, path)
		}
	}
	missing = unconfirmed
	expected.BaselineLegacyUnconfirmed = observation.LegacyUnconfirmed
	p, err := subtitle.PlanLibraryAssignments(ctx, season, operation, known, missing, admission)
	if err != nil {
		return nil, err
	}
	if len(p.Media) != count {
		return nil, errors.New("current season media differs from A30 set")
	}
	for _, media := range p.LegacyEntries {
		if identities[media.Path] != strconv.FormatInt(media.Identity, 10) {
			return nil, errors.New("a30_recorded_identity_invalid: identity differs from current media")
		}
	}
	return &bootstrapPlan{Schema: 1, A30Path: a30, DBPath: db, ObserverPath: observer, Expected: expected, Plan: p, LegacyApprovalPath: approvalPath, LegacyApprovalBytes: approvalBytes}, nil
}
func run(args []string) error {
	command := "plan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	f := flag.NewFlagSet("season-bootstrap "+command, flag.ContinueOnError)
	a30 := f.String("a30-evidence", "", "既有A30私有evidence路径")
	db := f.String("db", "", "既有pipeline DB，仅只读")
	observer := f.String("observer", "", "新鲜observer快照")
	season := f.String("season", "", "精确season绝对路径")
	operation := f.String("operation", "", "显式bootstrap operation")
	planPath := f.String("plan", "", "私有plan JSON")
	proofPath := f.String("maintenance-proof", "", "所有writer兼容/无旧writer的当次证明")
	legacyApproval := f.String("legacy-unconfirmed-approval", "", "精确旧未索引条目的私有批准文件；apply时须显式重申同一路径")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	ctx := context.Background()
	switch command {
	case "inspect":
		l, err := subtitle.LoadLibraryAssignments(*season)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(l)
	case "plan":
		p, err := prepare(ctx, *a30, *db, *observer, *season, *operation, *legacyApproval)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(p)
	case "apply":
		var p bootstrapPlan
		if err := readJSON(*planPath, &p); err != nil {
			return err
		}
		if p.Schema != 1 || p.Plan == nil || p.Plan.OperationID != *operation {
			return errors.New("explicit operation/plan mismatch")
		}
		if p.Plan.LegacyAdmission != nil {
			if *legacyApproval == "" || *legacyApproval != p.LegacyApprovalPath || *legacyApproval != p.Plan.LegacyAdmission.ApprovalPath {
				return errors.New("legacy approval path changed or not explicitly confirmed")
			}
			admission, raw, err := readLegacyAdmission(*legacyApproval)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, p.LegacyApprovalBytes) || !sameLegacyAdmission(admission, p.Plan.LegacyAdmission) || !sameLegacyAdmission(admission, p.Expected.LegacyAdmission) {
				return errors.New("legacy approval content changed")
			}
		} else if *legacyApproval != "" || p.LegacyApprovalPath != "" || len(p.LegacyApprovalBytes) > 0 || p.Expected.LegacyAdmission != nil {
			return errors.New("unexpected legacy approval inputs")
		}
		var proof maintenanceProof
		if err := readJSON(*proofPath, &proof); err != nil {
			return err
		}
		age := time.Since(proof.VerifiedAt)
		if !proof.AllWritersLedgerAware || !proof.NoSeasonWriter || proof.Operator == "" || proof.DeploymentReference == "" || proof.VerifiedAt.IsZero() || age > 2*time.Minute || age < -5*time.Second {
			return errors.New("fresh all-writers-ledger-aware maintenance proof required")
		}
		current, err := prepare(ctx, p.A30Path, p.DBPath, p.ObserverPath, p.Plan.SeasonPath, p.Plan.OperationID, *legacyApproval)
		if err != nil {
			return err
		}
		before, _ := json.Marshal(p.Plan)
		now, _ := json.Marshal(current.Plan)
		if string(before) != string(now) {
			return errors.New("bootstrap original bytes changed")
		}
		if p.Plan.LegacyAdmission != nil {
			_, raw, err := readLegacyAdmission(*legacyApproval)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, p.LegacyApprovalBytes) {
				return errors.New("legacy approval content changed before apply")
			}
		}
		if err = subtitle.ApplyLibraryAssignments(ctx, p.Plan); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"season": p.Plan.SeasonPath, "operation": p.Plan.OperationID, "state": "append_enabled", "production_verified": false})
	default:
		return errors.New("only plan/apply/inspect")
	}
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
