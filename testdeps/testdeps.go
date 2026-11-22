// Package testdeps gives integration tests their Postgres and RabbitMQ.
// A missing dependency fails the test when CI is set and skips it otherwise.
package testdeps

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

// How long to wait for a dependency before calling it unreachable.
const dialTimeout = 3 * time.Second

// InCI reports whether the CI variable is set to anything but "false" or "0".
func InCI() bool {
	v := os.Getenv("CI")
	return v != "" && v != "false" && v != "0"
}

// Unavailable fails the test in CI and skips it locally, naming the missing dependency.
func Unavailable(t testing.TB, dependency string, err error) {
	t.Helper()
	if InCI() {
		t.Fatalf("%s is unreachable and CI is set, so this test fails instead of skipping: %v", dependency, err)
	}
	t.Skipf("skipping: %s is unreachable (set CI=true to fail instead): %v", dependency, err)
}

// Postgres creates an empty database on the server named by the PG_DB_* variables
// and drops it when the test ends.
func Postgres(t testing.TB) *bun.DB {
	t.Helper()
	ctx := context.Background()
	admin := openPostgres("postgres")
	pingCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	if err := admin.PingContext(pingCtx); err != nil {
		admin.Close()
		Unavailable(t, "postgres at "+postgresAddr(), err)
		return nil
	}

	name := "testdeps_" + randomHex()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	db := bun.NewDB(openPostgres(name), pgdialect.New())
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})
	return db
}

// RabbitMQ returns the AMQP URL from the AMQP_* variables once the broker accepts connections.
func RabbitMQ(t testing.TB) string {
	t.Helper()
	host, port := env("AMQP_HOST", "localhost"), env("AMQP_PORT", "5672")
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), dialTimeout)
	if err != nil {
		Unavailable(t, "rabbitmq at "+net.JoinHostPort(host, port), err)
		return ""
	}
	conn.Close()
	return fmt.Sprintf("%s://%s:%s@%s/",
		env("AMQP_PROTOCOL", "amqp"),
		url.QueryEscape(env("AMQP_USER", "guest")),
		url.QueryEscape(env("AMQP_PASSWORD", "guest")),
		net.JoinHostPort(host, port))
}

func openPostgres(database string) *sql.DB {
	dsn := fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable",
		url.QueryEscape(env("PG_DB_USER", "postgres")),
		url.QueryEscape(env("PG_DB_PASSWORD", "!ChangeMe!")),
		postgresAddr(),
		database)
	return sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn), pgdriver.WithDialTimeout(dialTimeout)))
}

func postgresAddr() string {
	return net.JoinHostPort(env("PG_DB_HOST", "127.0.0.1"), env("PG_DB_PORT", "5432"))
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func randomHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
