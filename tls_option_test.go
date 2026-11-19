package boilerplates

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/uptrace/bun/driver/pgdriver"
)

func TestBuildTLSOptionWarnsOnlyInRequireMode(t *testing.T) {
	cases := map[string]struct {
		insecure bool
		tls      bool
		warns    int
	}{
		"require":     {insecure: true, tls: true, warns: 1},
		"verify-ca":   {insecure: false, tls: true, warns: 0},
		"verify-full": {insecure: false, tls: true, warns: 0},
		"disable":     {tls: false, warns: 0},
	}
	for mode, tc := range cases {
		t.Run(mode, func(t *testing.T) {
			logger, logs := jsonLogger()
			opt := buildTLSOption(&DatabaseConfig{SSLMode: mode, Host: "pgbouncer"}, logger)

			cfg := pgdriver.NewConnector(opt).Config().TLSConfig
			if (cfg != nil) != tc.tls {
				t.Fatalf("TLS enabled = %v, want %v", cfg != nil, tc.tls)
			}
			if cfg != nil && cfg.InsecureSkipVerify != tc.insecure {
				t.Fatalf("InsecureSkipVerify = %v, want %v", cfg.InsecureSkipVerify, tc.insecure)
			}

			out := strings.TrimSpace(logs.String())
			var lines []string
			if out != "" {
				lines = strings.Split(out, "\n")
			}
			if len(lines) != tc.warns {
				t.Fatalf("got %d log line(s), want %d: %q", len(lines), tc.warns, out)
			}
			if tc.warns == 0 {
				return
			}
			var entry map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
				t.Fatalf("line %q is not JSON: %v", lines[0], err)
			}
			if entry["level"] != "warning" || entry["component"] != "postgres" || entry["ssl_mode"] != "require" {
				t.Fatalf("unexpected warning fields: %v", entry)
			}
			if !strings.Contains(entry["msg"].(string), "verify-ca") {
				t.Fatalf("warning does not advise verify-ca: %q", entry["msg"])
			}
		})
	}
}
