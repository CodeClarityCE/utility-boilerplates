package boilerplates

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/uptrace/bun/driver/pgdriver"
)

const (
	dbConnectDefaultMaxWait = 120 * time.Second
	dbConnectAttemptTimeout = 5 * time.Second
	dbConnectFirstBackoff   = 500 * time.Millisecond
	dbConnectMaxBackoff     = 5 * time.Second
)

// connectRetry bounds how long startup waits for a database to answer.
type connectRetry struct {
	maxWait        time.Duration
	attemptTimeout time.Duration
	firstBackoff   time.Duration
	maxBackoff     time.Duration
}

// defaultConnectRetry returns the startup retry settings, with the window read from the environment.
func defaultConnectRetry() connectRetry {
	return connectRetry{
		maxWait:        dbConnectMaxWait(),
		attemptTimeout: dbConnectAttemptTimeout,
		firstBackoff:   dbConnectFirstBackoff,
		maxBackoff:     dbConnectMaxBackoff,
	}
}

// dbConnectMaxWait reads DB_CONNECT_MAX_WAIT_SECONDS, falling back to 120s when unset or invalid.
func dbConnectMaxWait() time.Duration {
	v := os.Getenv("DB_CONNECT_MAX_WAIT_SECONDS")
	if v == "" {
		return dbConnectDefaultMaxWait
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		logrus.WithFields(logrus.Fields{
			"value":   v,
			"default": dbConnectDefaultMaxWait.String(),
		}).Warn("Invalid DB_CONNECT_MAX_WAIT_SECONDS, using the default")
		return dbConnectDefaultMaxWait
	}
	return time.Duration(secs) * time.Second
}

// pingUntilReady pings until the database answers, the window runs out, or the error cannot be fixed by waiting.
func (r connectRetry) pingUntilReady(database string, ping func(context.Context) error) error {
	deadline := time.Now().Add(r.maxWait)
	backoff := r.firstBackoff
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), r.attemptTimeout)
		err := ping(ctx)
		cancel()
		if err == nil {
			if attempt > 1 {
				logrus.WithFields(logrus.Fields{"database": database, "attempts": attempt}).
					Info("Connected to the database after retrying")
			}
			return nil
		}
		if !isRetryableDBError(err) {
			return fmt.Errorf("database ping failed: %w", err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("database ping failed after %d attempt(s) in %s: %w", attempt, r.maxWait, err)
		}
		delay := min(backoff, remaining)
		logrus.WithFields(logrus.Fields{
			"database":   database,
			"attempt":    attempt,
			"error":      err.Error(),
			"next_delay": delay.String(),
		}).Warn("Database not ready, retrying")
		time.Sleep(delay)
		backoff = min(backoff*2, r.maxBackoff)
	}
}

// serverError is an error sent by Postgres or pgbouncer, such as pgdriver.Error.
type serverError interface {
	error
	Field(k byte) string
}

var _ serverError = pgdriver.Error{}

// isRetryableDBError reports whether waiting could fix the error; bad credentials and a missing database cannot.
func isRetryableDBError(err error) bool {
	var pgErr serverError
	if !errors.As(err, &pgErr) {
		return true
	}
	switch pgErr.Field('C') {
	case "28000", "28P01", "3D000":
		return false
	}
	// pgbouncer reports these with its own generic code.
	msg := strings.ToLower(pgErr.Field('M'))
	for _, s := range []string{"password authentication failed", "no such database", "no such user"} {
		if strings.Contains(msg, s) {
			return false
		}
	}
	return true
}
