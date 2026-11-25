package boilerplates

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// freePort returns a port nothing listens on right now.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	return port
}

// metricsStatus polls GET /metrics on the port and returns the last status, or 0.
func metricsStatus(port string) int {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://127.0.0.1:" + port + "/metrics")
		if err == nil {
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

func TestStartMetricsServerRetriesAfterAFailedBindThenStartsOnce(t *testing.T) {
	port := freePort(t)

	// Something else holds the port, so the first start cannot bind
	blocker, err := net.Listen("tcp", ":"+port)
	if err != nil {
		t.Fatal(err)
	}
	StartMetricsServer(port)
	_ = blocker.Close()

	// Starting again after the failed bind must still work, from many goroutines at once
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			StartMetricsServer(port)
		}()
	}
	wg.Wait()
	StartMetricsServer(port)

	if status := metricsStatus(port); status != http.StatusOK {
		t.Fatalf("GET /metrics returned %d, want 200", status)
	}
}
