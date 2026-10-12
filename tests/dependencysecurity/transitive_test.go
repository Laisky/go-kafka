package dependencysecurity_test

import (
	"bytes"
	"fmt"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type captureLogger struct{ bytes.Buffer }

func (l *captureLogger) Printf(format string, args ...interface{}) {
	fmt.Fprintf(&l.Buffer, format, args...)
}
func TestRetryableHTTPRedactsURLPassword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	logger := &captureLogger{}
	client := retryablehttp.NewClient()
	client.Logger = logger
	client.RetryMax = 0
	url := strings.Replace(server.URL, "http://", "http://synthetic-user:synthetic-secret@", 1)
	req, err := retryablehttp.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if strings.Contains(logger.String(), "synthetic-secret") {
		t.Fatal("URL password appeared in retry log")
	}
}
func TestPromHTTPBoundsUnknownMethodLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "synthetic_requests_total", Help: "Synthetic regression"}, []string{"method"})
	registry.MustRegister(counter)
	handler := promhttp.InstrumentHandlerCounter(counter, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for i := 0; i < 16; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(fmt.Sprintf("SYNTHETIC%d", i), "/", nil))
	}
	metrics, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || len(metrics[0].Metric) != 1 {
		t.Fatalf("unknown methods created unbounded series: %v", metrics)
	}
}
func TestLogrusWriterAcceptsLongLine(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	writer := logger.Writer()
	defer writer.Close()
	done := make(chan error, 1)
	go func() { _, err := io.WriteString(writer, strings.Repeat("x", 96<<10)+"\n"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("long log line failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long log line blocked writer")
	}
}
