package boilerplates

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// testRetry keeps the windows short so the tests run fast; its window starts now.
func testRetry(maxWait time.Duration) connectRetry {
	return connectRetry{
		maxWait:        maxWait,
		attemptTimeout: 50 * time.Millisecond,
		firstBackoff:   10 * time.Millisecond,
		maxBackoff:     40 * time.Millisecond,
		deadline:       time.Now().Add(maxWait),
		log:            logrus.New(),
	}
}

// jsonLogger writes JSON lines into the returned buffer.
func jsonLogger() (*logrus.Logger, *bytes.Buffer) {
	var logs bytes.Buffer
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetOutput(&logs)
	return logger, &logs
}

// fakeServerError stands in for pgdriver.Error, whose fields can't be set outside its package.
type fakeServerError map[byte]string

func (e fakeServerError) Error() string       { return e['M'] }
func (e fakeServerError) Field(k byte) string { return e[k] }

func pgError(code, msg string) error {
	return fmt.Errorf("pgdriver: %w", fakeServerError{'C': code, 'M': msg})
}

func TestPingUntilReadySucceedsWhenDatabaseComesUp(t *testing.T) {
	readyAt := time.Now().Add(100 * time.Millisecond)
	attempts := 0
	ping := func(context.Context) error {
		attempts++
		if time.Now().Before(readyAt) {
			return errors.New("dial tcp pgbouncer:6432: connect: connection refused")
		}
		return nil
	}

	if err := testRetry(2*time.Second).pingUntilReady("codeclarity", ping); err != nil {
		t.Fatalf("expected success once the database answers, got %v", err)
	}
	if attempts < 2 {
		t.Fatalf("expected at least one retry, got %d attempt(s)", attempts)
	}
}

func TestPingUntilReadyGivesUpAfterWindow(t *testing.T) {
	r := testRetry(300 * time.Millisecond)
	ping := func(ctx context.Context) error {
		<-ctx.Done() // a hung login that only ends at the attempt timeout
		return errors.New("pgdriver: SASL: read tcp: i/o timeout")
	}

	start := time.Now()
	err := r.pingUntilReady("knowledge", ping)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when the database never answers")
	}
	if !strings.Contains(err.Error(), "i/o timeout") {
		t.Fatalf("expected the last cause in the error, got %v", err)
	}
	if elapsed < r.maxWait || elapsed > r.maxWait+r.attemptTimeout+50*time.Millisecond {
		t.Fatalf("expected to give up after the window plus one attempt, took %s", elapsed)
	}
}

func TestPingUntilReadyFailsAtOnceOnUnfixableErrors(t *testing.T) {
	cases := map[string]error{
		"bad password":          pgError("28P01", `password authentication failed for user "codeclarity"`),
		"missing database":      pgError("3D000", `database "nope" does not exist`),
		"pgbouncer bad login":   pgError("08P01", "password authentication failed"),
		"pgbouncer missing db":  pgError("08P01", "no such database: nope"),
		"untrusted certificate": fmt.Errorf("pgdriver: %w", x509.UnknownAuthorityError{}),
		"wrong host name":       fmt.Errorf("pgdriver: %w", x509.HostnameError{Host: "pgbouncer"}),
		"flattened TLS error":   errors.New("pgdriver: tls: failed to verify certificate: x509: certificate signed by unknown authority"),
		"server without TLS":    errors.New("pgdriver: server does not support SSL"),
		"empty user":            errors.New("pgdriver: User option is empty (to configure, use WithUser)."),
	}
	for name, cause := range cases {
		t.Run(name, func(t *testing.T) {
			attempts := 0
			ping := func(context.Context) error { attempts++; return cause }

			err := testRetry(5*time.Second).pingUntilReady("plugins", ping)
			if err == nil || !errors.Is(err, cause) {
				t.Fatalf("expected the cause back, got %v", err)
			}
			if attempts != 1 {
				t.Fatalf("expected no retry, got %d attempt(s)", attempts)
			}
		})
	}
}

func TestPingUntilReadyRetriesServerStartup(t *testing.T) {
	attempts := 0
	ping := func(context.Context) error {
		attempts++
		if attempts == 1 {
			return pgError("57P03", "the database system is starting up")
		}
		return nil
	}
	if err := testRetry(time.Second).pingUntilReady("codeclarity", ping); err != nil {
		t.Fatalf("expected a retry past startup, got %v", err)
	}
}

func TestDatabasesShareOneDeadline(t *testing.T) {
	r := testRetry(200 * time.Millisecond)
	down := func(context.Context) error { return errors.New("dial tcp pgbouncer:6432: connect: connection refused") }

	start := time.Now()
	_ = r.pingUntilReady("codeclarity", down)
	_ = r.pingUntilReady("knowledge", down)
	_ = r.pingUntilReady("plugins", down)

	if elapsed := time.Since(start); elapsed > r.maxWait+r.attemptTimeout*3+100*time.Millisecond {
		t.Fatalf("three databases waited %s, want about one window of %s", elapsed, r.maxWait)
	}
}

func TestRetryLinesAreJSON(t *testing.T) {
	logger, logs := jsonLogger()
	r := testRetry(time.Second)
	r.log = logger
	attempts := 0
	ping := func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("dial tcp pgbouncer:6432: connect: connection refused")
		}
		return nil
	}

	if err := r.pingUntilReady("knowledge", ping); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected two retry lines and one success line, got %q", logs.String())
	}
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %q is not JSON: %v", line, err)
		}
		if entry["database"] != "knowledge" {
			t.Fatalf("line %q has no database field", line)
		}
	}
}

func TestDBConnectMaxWait(t *testing.T) {
	cases := map[string]struct {
		value string
		want  time.Duration
		warn  bool
	}{
		"unset":    {"", dbConnectDefaultMaxWait, false},
		"valid":    {"30", 30 * time.Second, false},
		"zero":     {"0", dbConnectDefaultMaxWait, true},
		"negative": {"-5", dbConnectDefaultMaxWait, true},
		"garbage":  {"soon", dbConnectDefaultMaxWait, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DB_CONNECT_MAX_WAIT_SECONDS", tc.value)
			logger, logs := jsonLogger()

			if got := dbConnectMaxWait(logger); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
			if warned := strings.Contains(logs.String(), "DB_CONNECT_MAX_WAIT_SECONDS"); warned != tc.warn {
				t.Fatalf("warning logged = %v, want %v (logs: %q)", warned, tc.warn, logs.String())
			}
		})
	}
}
