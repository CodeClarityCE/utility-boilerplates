package boilerplates

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	plugin_db "github.com/CodeClarityCE/utility-types/plugin_db"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

// Same table as the API's plugins migrations create.
const pluginTableDDL = `CREATE TABLE "plugin" ("id" uuid NOT NULL DEFAULT uuid_generate_v4(), "name" character varying, "version" character varying, "description" character varying, "depends_on" jsonb, "config" jsonb, CONSTRAINT "PK_26c98f1bfcce9906f8bb0b11e5b" PRIMARY KEY ("id"))`
const pluginUniqueNameDDL = `ALTER TABLE "plugin" ADD CONSTRAINT "UQ_plugin_name" UNIQUE ("name")`

func testPgEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func openTestDB(t *testing.T, database string) *bun.DB {
	t.Helper()
	sqldb := sql.OpenDB(pgdriver.NewConnector(
		pgdriver.WithAddr(testPgEnv("TEST_PG_HOST", "localhost")+":"+testPgEnv("TEST_PG_PORT", "5432")),
		pgdriver.WithUser(testPgEnv("TEST_PG_USER", "postgres")),
		pgdriver.WithPassword(testPgEnv("TEST_PG_PASSWORD", "!ChangeMe!")),
		pgdriver.WithDatabase(database),
		pgdriver.WithInsecure(true),
		pgdriver.WithTimeout(5*time.Second),
	))
	return bun.NewDB(sqldb, pgdialect.New())
}

// scratchRegistry creates a throwaway plugins database and returns its name.
// The test is skipped when no Postgres is reachable.
func scratchRegistry(t *testing.T, unique bool) string {
	t.Helper()
	admin := openTestDB(t, "postgres")
	defer admin.Close()
	ctx := context.Background()
	if err := admin.PingContext(ctx); err != nil {
		t.Skipf("no Postgres reachable for registry tests: %v", err)
	}
	name := fmt.Sprintf("test_plugin_registry_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		a := openTestDB(t, "postgres")
		defer a.Close()
		_, _ = a.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	db := openTestDB(t, name)
	defer db.Close()
	stmts := []string{`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`, pluginTableDDL}
	if unique {
		stmts = append(stmts, pluginUniqueNameDDL)
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("prepare registry schema: %v", err)
		}
	}
	return name
}

func registryRows(t *testing.T, db *bun.DB, name string) []plugin_db.Plugin {
	t.Helper()
	var rows []plugin_db.Plugin
	if err := db.NewSelect().Model(&rows).Where("name = ?", name).Scan(context.Background()); err != nil {
		t.Fatalf("read registry: %v", err)
	}
	return rows
}

func testPluginConfig(version string) plugin_db.Plugin {
	return plugin_db.Plugin{
		Name:        "js-sbom",
		Version:     version,
		Description: "Generates an SBOM",
		DependsOn:   []string{},
		Config:      map[string]any{"project": map[string]any{"type": "string"}},
	}
}

// Each replica has its own connection pool, as separate containers would.
func registerReplicasTogether(t *testing.T, database string, replicas int) []error {
	t.Helper()
	dbs := make([]*bun.DB, replicas)
	for i := range dbs {
		dbs[i] = openTestDB(t, database)
		if err := dbs[i].Ping(); err != nil {
			t.Fatalf("replica connect: %v", err)
		}
		defer dbs[i].Close()
	}
	start := make(chan struct{})
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	for i := range dbs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = registerPlugin(testPluginConfig("1.0.0"), dbs[i])
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

func TestFourReplicasAgainstEmptyRegistryLeaveOneRow(t *testing.T) {
	// Several rounds, since one round of a race can pass by luck.
	for round := 0; round < 10; round++ {
		database := scratchRegistry(t, true)
		for i, err := range registerReplicasTogether(t, database, 4) {
			if err != nil {
				t.Fatalf("round %d: replica %d failed to register: %v", round, i, err)
			}
		}
		db := openTestDB(t, database)
		rows := registryRows(t, db, "js-sbom")
		db.Close()
		if len(rows) != 1 {
			t.Fatalf("round %d: want 1 registry row, got %d", round, len(rows))
		}
	}
}

func TestRegisteringANewVersionUpdatesTheExistingRow(t *testing.T) {
	database := scratchRegistry(t, true)
	db := openTestDB(t, database)
	defer db.Close()

	if err := registerPlugin(testPluginConfig("1.0.0"), db); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	first := registryRows(t, db, "js-sbom")

	next := testPluginConfig("1.1.0")
	next.Description = "Generates a CycloneDX SBOM"
	next.DependsOn = []string{"downloader"}
	if err := registerPlugin(next, db); err != nil {
		t.Fatalf("second registration: %v", err)
	}

	rows := registryRows(t, db, "js-sbom")
	if len(rows) != 1 {
		t.Fatalf("want 1 registry row, got %d", len(rows))
	}
	got := rows[0]
	if got.Id != first[0].Id {
		t.Errorf("row id changed from %s to %s", first[0].Id, got.Id)
	}
	if got.Version != "1.1.0" || got.Description != "Generates a CycloneDX SBOM" {
		t.Errorf("row not updated: version %q, description %q", got.Version, got.Description)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "downloader" {
		t.Errorf("depends_on not updated: %v", got.DependsOn)
	}
}

func TestRegistrationWorksBeforeTheUniqueConstraintMigration(t *testing.T) {
	database := scratchRegistry(t, false)
	db := openTestDB(t, database)
	defer db.Close()

	for i := 0; i < 2; i++ {
		if err := registerPlugin(testPluginConfig("1.0.0"), db); err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	if rows := registryRows(t, db, "js-sbom"); len(rows) != 1 {
		t.Fatalf("want 1 registry row, got %d", len(rows))
	}
}

func TestAnOlderReplicaDoesNotDowngradeTheRow(t *testing.T) {
	database := scratchRegistry(t, true)
	db := openTestDB(t, database)
	defer db.Close()

	newer := testPluginConfig("v0.0.10-alpha")
	newer.Description = "Newer description"
	if err := registerPlugin(newer, db); err != nil {
		t.Fatalf("newer registration: %v", err)
	}
	older := testPluginConfig("v0.0.9-alpha")
	older.Description = "Older description"
	if err := registerPlugin(older, db); err != nil {
		t.Fatalf("older registration: %v", err)
	}

	got := registryRows(t, db, "js-sbom")[0]
	if got.Version != "v0.0.10-alpha" || got.Description != "Newer description" {
		t.Errorf("row downgraded: version %q, description %q", got.Version, got.Description)
	}
}

func TestTheSameVersionRefreshesTheRow(t *testing.T) {
	database := scratchRegistry(t, true)
	db := openTestDB(t, database)
	defer db.Close()

	if err := registerPlugin(testPluginConfig("1.0.0"), db); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	again := testPluginConfig("1.0.0")
	again.Description = "Fixed description"
	if err := registerPlugin(again, db); err != nil {
		t.Fatalf("second registration: %v", err)
	}

	if got := registryRows(t, db, "js-sbom")[0]; got.Description != "Fixed description" {
		t.Errorf("same version did not refresh the row: %q", got.Description)
	}
}

func TestMixedVersionReplicasStartingTogetherKeepTheNewest(t *testing.T) {
	for round := 0; round < 10; round++ {
		database := scratchRegistry(t, true)
		versions := []string{"1.0.0", "1.1.0", "1.0.0", "1.1.0"}
		dbs := make([]*bun.DB, len(versions))
		for i := range dbs {
			dbs[i] = openTestDB(t, database)
			if err := dbs[i].Ping(); err != nil {
				t.Fatalf("replica connect: %v", err)
			}
			defer dbs[i].Close()
		}
		start := make(chan struct{})
		errs := make([]error, len(versions))
		var wg sync.WaitGroup
		for i := range dbs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = registerPlugin(testPluginConfig(versions[i]), dbs[i])
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: replica %d: %v", round, i, err)
			}
		}
		rows := registryRows(t, dbs[0], "js-sbom")
		if len(rows) != 1 || rows[0].Version != "1.1.0" {
			t.Fatalf("round %d: want one 1.1.0 row, got %+v", round, rows)
		}
	}
}
