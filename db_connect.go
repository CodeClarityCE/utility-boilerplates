package boilerplates

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

// connectRetry bounds how long startup waits for its databases to answer.
// Every database shares one deadline, so startup waits at most maxWait in all.
type connectRetry struct {
	maxWait        time.Duration
	attemptTimeout time.Duration
	firstBackoff   time.Duration
	maxBackoff     time.Duration
	deadline       time.Time
	log            logrus.FieldLogger
}

// defaultConnectRetry returns the startup retry settings, with the window read from the environment
// and starting now. log receives the retry lines.
func defaultConnectRetry(log logrus.FieldLogger) connectRetry {
	maxWait := dbConnectMaxWait(log)
	return connectRetry{
		maxWait:        maxWait,
		attemptTimeout: dbConnectAttemptTimeout,
		firstBackoff:   dbConnectFirstBackoff,
		maxBackoff:     dbConnectMaxBackoff,
		deadline:       time.Now().Add(maxWait),
		log:            log,
	}
}

// dbConnectMaxWait reads DB_CONNECT_MAX_WAIT_SECONDS, falling back to 120s when unset or invalid.
func dbConnectMaxWait(log logrus.FieldLogger) time.Duration {
	v := os.Getenv("DB_CONNECT_MAX_WAIT_SECONDS")
	if v == "" {
		return dbConnectDefaultMaxWait
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		log.WithFields(logrus.Fields{
			"value":   v,
			"default": dbConnectDefaultMaxWait.String(),
		}).Warn("Invalid DB_CONNECT_MAX_WAIT_SECONDS, using the default")
		return dbConnectDefaultMaxWait
	}
	return time.Duration(secs) * time.Second
}

// pingUntilReady pings until the database answers, the shared window runs out, or the error cannot be fixed by waiting.
func (r connectRetry) pingUntilReady(database string, ping func(context.Context) error) error {
	deadline := r.deadline
	backoff := r.firstBackoff
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), r.attemptTimeout)
		err := ping(ctx)
		cancel()
		if err == nil {
			if attempt > 1 {
				r.log.WithFields(logrus.Fields{"database": database, "attempts": attempt}).
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
		r.log.WithFields(logrus.Fields{
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

// isRetryableDBError reports whether waiting could fix the error. Bad credentials, a missing
// database or user name, and TLS or certificate failures cannot.
func isRetryableDBError(err error) bool {
	if isTLSError(err) {
		return false
	}
	if strings.Contains(strings.ToLower(err.Error()), "user option is empty") {
		return false
	}
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

// isTLSError reports whether err comes from the TLS handshake or certificate checks.
func isTLSError(err error) bool {
	var (
		verifyErr    *tls.CertificateVerificationError
		alertErr     tls.AlertError
		recordErr    tls.RecordHeaderError
		authorityErr x509.UnknownAuthorityError
		hostnameErr  x509.HostnameError
		invalidErr   x509.CertificateInvalidError
	)
	if errors.As(err, &verifyErr) || errors.As(err, &alertErr) || errors.As(err, &recordErr) ||
		errors.As(err, &authorityErr) || errors.As(err, &hostnameErr) || errors.As(err, &invalidErr) {
		return true
	}
	// Some drivers flatten the cause into text.
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"x509:", "tls:", "does not support ssl", "does not support tls"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
