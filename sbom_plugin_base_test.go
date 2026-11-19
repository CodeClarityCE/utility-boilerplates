package boilerplates

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	types_amqp "github.com/CodeClarityCE/utility-types/amqp"
	codeclarity "github.com/CodeClarityCE/utility-types/codeclarity_db"
	plugin_db "github.com/CodeClarityCE/utility-types/plugin_db"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// fakeConnector accepts every statement so result inserts need no database.
type fakeConnector struct{}

func (fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn{}, nil }
func (fakeConnector) Driver() driver.Driver                        { return nil }

type fakeConn struct{}

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (fakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &idRows{}, nil
}

// idRows answers INSERT ... RETURNING with one generated id.
type idRows struct{ done bool }

func (*idRows) Columns() []string { return []string{"id"} }
func (*idRows) Close() error      { return nil }
func (r *idRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = uuid.NewString()
	return nil
}

type fakeOutput struct{ status codeclarity.AnalysisStatus }

func (o fakeOutput) GetStatus() codeclarity.AnalysisStatus { return o.status }
func (o fakeOutput) GetFramework() string                  { return "" }

type fakeAnalyzer struct {
	canAnalyze bool
	err        error
	status     codeclarity.AnalysisStatus
	output     map[string]any
}

func (a fakeAnalyzer) AnalyzeProject(string, string, any) (SBOMOutput, error) {
	return fakeOutput{a.status}, a.err
}
func (a fakeAnalyzer) CanAnalyze(string) bool                 { return a.canAnalyze }
func (a fakeAnalyzer) GetLanguage() string                    { return "PHP" }
func (a fakeAnalyzer) DetectFramework(string) string          { return "" }
func (a fakeAnalyzer) ConvertToMap(SBOMOutput) map[string]any { return a.output }
func (a fakeAnalyzer) GetDependencyCount(SBOMOutput) int      { return 0 }

func runHandler(t *testing.T, analyzer fakeAnalyzer, project string) (map[string]any, codeclarity.AnalysisStatus) {
	t.Helper()
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOWNLOAD_PATH", base)

	db := bun.NewDB(sql.OpenDB(fakeConnector{}), pgdialect.New())
	defer db.Close()
	handler := &GenericSBOMHandler{Analyzer: analyzer}
	result, status, err := handler.StartAnalysis(
		&PluginDatabases{Codeclarity: db},
		types_amqp.DispatcherPluginMessage{AnalysisId: uuid.New()},
		plugin_db.Plugin{Name: "php-sbom"},
		codeclarity.Analysis{Config: map[string]any{"php-sbom": map[string]any{"project": project}}},
	)
	if err != nil {
		t.Fatalf("StartAnalysis returned an error: %v", err)
	}
	return result, status
}

func publicErrors(descriptions ...string) map[string]any {
	errs := []map[string]any{}
	for _, d := range descriptions {
		errs = append(errs, map[string]any{
			"public_error":  map[string]any{"description": d},
			"private_error": map[string]any{"description": "private /private/secret/path"},
		})
	}
	return map[string]any{"analysis_info": map[string]any{"status": "failure", "errors": errs}}
}

func TestSBOMFailureReportsAReadableReason(t *testing.T) {
	cases := []struct {
		name     string
		analyzer fakeAnalyzer
		project  string
		want     string
	}{
		{
			name:     "missing project source",
			analyzer: fakeAnalyzer{canAnalyze: true},
			project:  "does-not-exist",
			want:     "the project source could not be found",
		},
		{
			name:     "no manifest for the language",
			analyzer: fakeAnalyzer{canAnalyze: false},
			project:  "project",
			want:     "no PHP manifest was found in the project",
		},
		{
			name:     "analyzer error stays in the logs",
			analyzer: fakeAnalyzer{canAnalyze: true, err: errors.New("open /private/org/composer.lock: denied")},
			project:  "project",
			want:     "the PHP dependencies could not be analyzed",
		},
		{
			name:     "failed output uses the latest public error",
			analyzer: fakeAnalyzer{canAnalyze: true, status: codeclarity.FAILURE, output: publicErrors("an older failure", "Unable to parse lock file, is it a valid lockfile?")},
			project:  "project",
			want:     "Unable to parse lock file, is it a valid lockfile?",
		},
		{
			name:     "failed output without a public error",
			analyzer: fakeAnalyzer{canAnalyze: true, status: codeclarity.FAILURE, output: publicErrors("")},
			project:  "project",
			want:     "the PHP dependencies could not be analyzed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, status := runHandler(t, tc.analyzer, tc.project)
			if status != codeclarity.FAILURE {
				t.Fatalf("status = %q, want failure", status)
			}
			got, _ := result[codeclarity.StepFailureReasonKey].(string)
			if got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "/private") || strings.Contains(got, os.TempDir()) {
				t.Errorf("reason leaks a path: %q", got)
			}
		})
	}
}

func TestSuccessfulSBOMReportsNoFailureReason(t *testing.T) {
	result, status := runHandler(t, fakeAnalyzer{canAnalyze: true, status: codeclarity.SUCCESS, output: map[string]any{}}, "project")
	if status != codeclarity.SUCCESS {
		t.Fatalf("status = %q, want success", status)
	}
	if _, found := result[codeclarity.StepFailureReasonKey]; found {
		t.Errorf("unexpected failure reason in %v", result)
	}
}

// folderAnalyzer is a JavaScript analyzer whose output lists the workspaces given per folder name.
type folderAnalyzer struct {
	workspaces map[string][]string
	failing    map[string]string
	analysed   *[]string
}

type folderOutput struct {
	folder string
	status codeclarity.AnalysisStatus
}

func (o folderOutput) GetStatus() codeclarity.AnalysisStatus { return o.status }
func (o folderOutput) GetFramework() string                  { return "" }

func (a folderAnalyzer) AnalyzeProject(path string, _ string, _ any) (SBOMOutput, error) {
	folder := filepath.Base(path)
	*a.analysed = append(*a.analysed, folder)
	if _, failed := a.failing[folder]; failed {
		return folderOutput{folder, codeclarity.FAILURE}, nil
	}
	return folderOutput{folder, codeclarity.SUCCESS}, nil
}
func (a folderAnalyzer) CanAnalyze(path string) bool {
	_, err := os.Stat(filepath.Join(path, "package.json"))
	return err == nil
}
func (a folderAnalyzer) GetLanguage() string           { return "JavaScript" }
func (a folderAnalyzer) DetectFramework(string) string { return "" }
func (a folderAnalyzer) ConvertToMap(output SBOMOutput) map[string]any {
	folder := output.(folderOutput).folder
	if reason, failed := a.failing[folder]; failed {
		return publicErrors(reason)
	}
	workspaces := map[string]any{}
	for _, w := range a.workspaces[folder] {
		workspaces[w] = map[string]any{"dependencies": map[string]any{}}
	}
	return map[string]any{
		"workspaces":    workspaces,
		"analysis_info": map[string]any{"status": "success", "package_manager": "NPM"},
	}
}
func (a folderAnalyzer) GetDependencyCount(output SBOMOutput) int {
	return len(a.workspaces[output.(folderOutput).folder])
}

// runFolders runs a JavaScript SBOM step over a project laid out with the given files.
func runFolders(t *testing.T, analyzer folderAnalyzer, files ...string) (map[string]any, codeclarity.AnalysisStatus) {
	t.Helper()
	base := t.TempDir()
	for _, f := range files {
		path := filepath.Join(base, "project", f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DOWNLOAD_PATH", base)
	db := bun.NewDB(sql.OpenDB(fakeConnector{}), pgdialect.New())
	defer db.Close()
	handler := &GenericSBOMHandler{Analyzer: analyzer}
	result, status, err := handler.StartAnalysis(
		&PluginDatabases{Codeclarity: db},
		types_amqp.DispatcherPluginMessage{AnalysisId: uuid.New()},
		plugin_db.Plugin{Name: "js-sbom"},
		codeclarity.Analysis{Id: uuid.New(), Config: map[string]any{"js-sbom": map[string]any{"project": "project"}}},
	)
	if err != nil {
		t.Fatalf("StartAnalysis returned an error: %v", err)
	}
	return result, status
}

func TestSubfolderProjectsBecomeWorkspaces(t *testing.T) {
	analysed := []string{}
	analyzer := folderAnalyzer{
		workspaces: map[string][]string{"api": {"."}, "frontend": {".", "packages/ui"}},
		analysed:   &analysed,
	}
	result, status := runFolders(t, analyzer,
		"api/package.json", "api/pnpm-lock.yaml", "frontend/package.json", "frontend/package-lock.json")
	if status != codeclarity.SUCCESS {
		t.Fatalf("status = %q, want success (%v)", status, result)
	}
	if !slices.Equal(analysed, []string{"api", "frontend"}) {
		t.Errorf("analysed %v, want api and frontend", analysed)
	}
	if got := result["workspaces"]; !slices.Equal(got.([]string), []string{"api", "frontend", "frontend/packages/ui"}) {
		t.Errorf("workspaces = %v", got)
	}
	if got := result["packageCount"]; got != 3 {
		t.Errorf("packageCount = %v, want 3", got)
	}
}

func TestRootManifestIsAnalysedAlone(t *testing.T) {
	analysed := []string{}
	analyzer := folderAnalyzer{workspaces: map[string][]string{"project": {".", "api"}}, analysed: &analysed}
	result, status := runFolders(t, analyzer, "package.json", "pnpm-lock.yaml", "api/package.json", "web/package.json", "web/yarn.lock")
	if status != codeclarity.SUCCESS {
		t.Fatalf("status = %q, want success (%v)", status, result)
	}
	if !slices.Equal(analysed, []string{"project"}) {
		t.Errorf("analysed %v, want only the root", analysed)
	}
	if got := result["workspaces"]; !slices.Equal(got.([]string), []string{".", "api"}) {
		t.Errorf("workspaces = %v", got)
	}
	if _, found := result["skipped_folders"]; found {
		t.Errorf("a root project reports skipped folders: %v", result["skipped_folders"])
	}
}

func TestSkippedSubfoldersAreReportedWithoutFailing(t *testing.T) {
	analysed := []string{}
	analyzer := folderAnalyzer{
		workspaces: map[string][]string{"api": {"."}},
		failing:    map[string]string{"broken": "Unable to parse lock file, is it a valid lockfile?"},
		analysed:   &analysed,
	}
	result, status := runFolders(t, analyzer,
		"api/package.json", "api/package-lock.json", "docs/package.json", "broken/package.json", "broken/yarn.lock")
	if status != codeclarity.SUCCESS {
		t.Fatalf("status = %q, want success (%v)", status, result)
	}
	if got := result["workspaces"]; !slices.Equal(got.([]string), []string{"api"}) {
		t.Errorf("workspaces = %v", got)
	}
	want := []SkippedFolder{
		{Path: "broken", Reason: "Unable to parse lock file, is it a valid lockfile?"},
		{Path: "docs", Reason: "package.json has no lockfile (package-lock.json, yarn.lock, pnpm-lock.yaml)"},
	}
	if got, _ := result["skipped_folders"].([]SkippedFolder); !slices.Equal(got, want) {
		t.Errorf("skipped_folders = %v, want %v", result["skipped_folders"], want)
	}
}

func TestEverySubfolderFailingFailsTheStep(t *testing.T) {
	analysed := []string{}
	analyzer := folderAnalyzer{failing: map[string]string{"a": "bad lockfile"}, analysed: &analysed}
	result, status := runFolders(t, analyzer, "a/package.json", "a/yarn.lock")
	if status != codeclarity.FAILURE {
		t.Fatalf("status = %q, want failure", status)
	}
	if got := result[codeclarity.StepFailureReasonKey]; got != "bad lockfile" {
		t.Errorf("reason = %v", got)
	}
}
