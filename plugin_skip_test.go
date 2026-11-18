package boilerplates

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	types_amqp "github.com/CodeClarityCE/utility-types/amqp"
	codeclarity "github.com/CodeClarityCE/utility-types/codeclarity_db"
	plugin_db "github.com/CodeClarityCE/utility-types/plugin_db"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// fakeAnalysisDB answers selects with one analysis row, or none, and records every write.
type fakeAnalysisDB struct {
	row    map[string]string
	writes *[]string
}

func (f fakeAnalysisDB) Connect(context.Context) (driver.Conn, error) { return analysisConn(f), nil }
func (fakeAnalysisDB) Driver() driver.Driver                          { return nil }

type analysisConn fakeAnalysisDB

func (analysisConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (analysisConn) Close() error                        { return nil }
func (c analysisConn) Begin() (driver.Tx, error) {
	*c.writes = append(*c.writes, "BEGIN")
	return nil, errors.New("not supported")
}
func (c analysisConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	*c.writes = append(*c.writes, query)
	return driver.RowsAffected(1), nil
}
func (c analysisConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &analysisRows{row: c.row}, nil
}

type analysisRows struct {
	row  map[string]string
	done bool
}

func (r *analysisRows) Columns() []string {
	cols := make([]string, 0, len(r.row))
	for c := range r.row {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	return cols
}
func (r *analysisRows) Close() error { return nil }
func (r *analysisRows) Next(dest []driver.Value) error {
	if r.row == nil || r.done {
		return io.EOF
	}
	r.done = true
	for i, c := range r.Columns() {
		dest[i] = []byte(r.row[c])
	}
	return nil
}

type recordingHandler struct{ called bool }

func (h *recordingHandler) StartAnalysis(*PluginDatabases, types_amqp.DispatcherPluginMessage, plugin_db.Plugin, codeclarity.Analysis) (map[string]any, codeclarity.AnalysisStatus, error) {
	h.called = true
	return map[string]any{}, codeclarity.SUCCESS, nil
}

// runCallback delivers one message for analysisId to a plugin whose database holds row.
func runCallback(t *testing.T, analysisId uuid.UUID, row map[string]string) (handler *recordingHandler, writes []string, logs string) {
	t.Helper()
	db := bun.NewDB(sql.OpenDB(fakeAnalysisDB{row: row, writes: &writes}), pgdialect.New())
	defer db.Close()
	var out bytes.Buffer
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetOutput(&out)
	config := plugin_db.Plugin{Name: "vuln-finder"}
	pb := &PluginBase{Config: config, DB: &PluginDatabases{Codeclarity: db}, Logger: logger}
	handler = &recordingHandler{}
	message, _ := json.Marshal(types_amqp.DispatcherPluginMessage{AnalysisId: analysisId})

	pb.createCallbackWrapper(handler)(nil, config, message)
	return handler, writes, out.String()
}

func assertOneInfoLine(t *testing.T, logs string, analysisId uuid.UUID) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one log line, got %q", logs)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("log line is not JSON: %q", lines[0])
	}
	if entry["level"] != "info" || entry["analysis_id"] != analysisId.String() {
		t.Errorf("expected an info line naming %s, got %v", analysisId, entry)
	}
}

func TestPluginSkipsADeletedAnalysis(t *testing.T) {
	id := uuid.New()
	handler, writes, logs := runCallback(t, id, nil)

	if handler.called {
		t.Error("the plugin ran a deleted analysis")
	}
	if len(writes) != 0 {
		t.Errorf("expected no writes, got %v", writes)
	}
	assertOneInfoLine(t, logs, id)
}

func TestPluginSkipsACancelledAnalysis(t *testing.T) {
	id := uuid.New()
	handler, writes, logs := runCallback(t, id, map[string]string{
		"id":     id.String(),
		"status": string(codeclarity.CANCELLED),
		"steps":  `[[{"Name":"vuln-finder","Status":"started"}]]`,
	})

	if handler.called {
		t.Error("the plugin ran a cancelled analysis")
	}
	if len(writes) != 0 {
		t.Errorf("expected no writes, got %v", writes)
	}
	assertOneInfoLine(t, logs, id)
}

func TestPluginRunsAStartedAnalysis(t *testing.T) {
	id := uuid.New()
	handler, _, _ := runCallback(t, id, map[string]string{
		"id":     id.String(),
		"status": string(codeclarity.STARTED),
		"steps":  `[[{"Name":"vuln-finder","Status":"started"}]]`,
	})

	if !handler.called {
		t.Error("the plugin did not run a started analysis")
	}
}
