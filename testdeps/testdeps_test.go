package testdeps_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/CodeClarityCE/utility-boilerplates/testdeps"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Set in the child process that runs one dependency test on its own.
const childEnv = "TESTDEPS_CHILD"

// runChild runs the named test of this package in a new process with the given extra env.
func runChild(t *testing.T, name string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.v")
	cmd.Env = append(withoutCI(os.Environ()), append([]string{childEnv + "=1"}, env...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// withoutCI drops CI from the env, so each child decides it on its own.
func withoutCI(env []string) []string {
	kept := []string{}
	for _, kv := range env {
		if !strings.HasPrefix(kv, "CI=") {
			kept = append(kept, kv)
		}
	}
	return kept
}

func TestChildUnreachablePostgres(t *testing.T) {
	if os.Getenv(childEnv) == "" {
		t.Skip("runs only as a child of the tests below")
	}
	testdeps.Postgres(t)
	t.Log("reached the database")
}

func TestUnreachablePostgresFailsInCI(t *testing.T) {
	out, err := runChild(t, "TestChildUnreachablePostgres", "CI=true", "PG_DB_HOST=127.0.0.1", "PG_DB_PORT=1")

	if err == nil || !strings.Contains(out, "--- FAIL") {
		t.Fatalf("want the test to fail in CI, got err=%v and output:\n%s", err, out)
	}
	if !strings.Contains(out, "postgres") {
		t.Errorf("want the failure to name the dependency, got:\n%s", out)
	}
}

func TestUnreachablePostgresSkipsLocally(t *testing.T) {
	out, err := runChild(t, "TestChildUnreachablePostgres", "PG_DB_HOST=127.0.0.1", "PG_DB_PORT=1")

	if err != nil || !strings.Contains(out, "--- SKIP") {
		t.Fatalf("want the test to skip locally, got err=%v and output:\n%s", err, out)
	}
}

func TestCIFalseCountsAsLocal(t *testing.T) {
	out, err := runChild(t, "TestChildUnreachablePostgres", "CI=false", "PG_DB_HOST=127.0.0.1", "PG_DB_PORT=1")

	if err != nil || !strings.Contains(out, "--- SKIP") {
		t.Fatalf("want CI=false to skip, got err=%v and output:\n%s", err, out)
	}
}

func TestPostgresGivesEachTestItsOwnDatabase(t *testing.T) {
	ctx := context.Background()
	first := testdeps.Postgres(t)
	second := testdeps.Postgres(t)

	_, err := first.ExecContext(ctx, "CREATE TABLE marker (id int)")
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	var count int
	err = second.QueryRowContext(ctx, "SELECT count(*) FROM pg_tables WHERE tablename = 'marker'").Scan(&count)
	if err != nil {
		t.Fatalf("query second database: %v", err)
	}
	if count != 0 {
		t.Errorf("want the second database to be separate, found the first one's table")
	}
}

func TestChildUnreachableRabbitMQ(t *testing.T) {
	if os.Getenv(childEnv) == "" {
		t.Skip("runs only as a child of the tests below")
	}
	testdeps.RabbitMQ(t)
	t.Log("reached the broker")
}

func TestUnreachableRabbitMQFailsInCI(t *testing.T) {
	out, err := runChild(t, "TestChildUnreachableRabbitMQ", "CI=true", "AMQP_HOST=127.0.0.1", "AMQP_PORT=1")

	if err == nil || !strings.Contains(out, "--- FAIL") || !strings.Contains(out, "rabbitmq") {
		t.Fatalf("want the test to fail in CI naming rabbitmq, got err=%v and output:\n%s", err, out)
	}
}

func TestUnreachableRabbitMQSkipsLocally(t *testing.T) {
	out, err := runChild(t, "TestChildUnreachableRabbitMQ", "AMQP_HOST=127.0.0.1", "AMQP_PORT=1")

	if err != nil || !strings.Contains(out, "--- SKIP") {
		t.Fatalf("want the test to skip locally, got err=%v and output:\n%s", err, out)
	}
}

func TestRabbitMQURLOpensAConnection(t *testing.T) {
	conn, err := amqp.Dial(testdeps.RabbitMQ(t))
	if err != nil {
		t.Fatalf("dial the returned URL: %v", err)
	}
	conn.Close()
}
