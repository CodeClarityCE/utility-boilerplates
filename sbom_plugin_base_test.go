package boilerplates

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
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
