package observability

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/proto"
)

type otlpRecorder struct {
	mu     sync.Mutex
	bodies map[string][][]byte
}

func newOTLPServer(t *testing.T) (*httptest.Server, *otlpRecorder) {
	recorder := &otlpRecorder{bodies: map[string][][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if r.Header.Get("Content-Encoding") == "gzip" {
			reader, err := gzip.NewReader(bytes.NewReader(body))
			require.NoError(t, err)
			body, err = io.ReadAll(reader)
			require.NoError(t, err)
		}
		recorder.mu.Lock()
		recorder.bodies[r.URL.Path] = append(recorder.bodies[r.URL.Path], body)
		recorder.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

func (r *otlpRecorder) requests(path string) [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[path]
}

func TestSetupDisabledIsNoop(t *testing.T) {
	t.Setenv("OTEL_ENABLED", "")
	shutdown, err := Setup(context.Background())
	require.NoError(t, err)
	require.NoError(t, shutdown(context.Background()))
}

func TestEnabled(t *testing.T) {
	t.Setenv("OTEL_ENABLED", "")
	assert.False(t, Enabled())
	t.Setenv("OTEL_ENABLED", "false")
	assert.False(t, Enabled())
	t.Setenv("OTEL_ENABLED", "true")
	assert.True(t, Enabled())
	t.Setenv("OTEL_ENABLED", "TRUE")
	assert.True(t, Enabled())
}

func TestServiceNameDefaultsAndOverride(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	assert.Equal(t, "expo-open-ota", ServiceName())
	t.Setenv("OTEL_SERVICE_NAME", "my-ota")
	assert.Equal(t, "my-ota", ServiceName())
}

func TestSetupRejectsUnknownProtocol(t *testing.T) {
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "carrier-pigeon")
	_, err := Setup(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OTEL_EXPORTER_OTLP_PROTOCOL")
}

func TestSetupGRPCProtocol(t *testing.T) {
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:1")
	shutdown, err := Setup(context.Background())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = shutdown(ctx) // nothing listening on the endpoint; flush errors are expected
}

func TestSetupExportsAllSignalsOverOTLPHTTP(t *testing.T) {
	server, recorder := newOTLPServer(t)
	t.Setenv("OTEL_ENABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)

	shutdown, err := Setup(context.Background())
	require.NoError(t, err)

	// Traces: one span through the global tracer provider.
	_, span := otel.Tracer("test").Start(context.Background(), "test-span")
	span.End()

	// Metrics: a Prometheus metric on the default registry must cross the
	// bridge onto OTLP untouched.
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "observability_bridge_test_total",
		Help: "test counter",
	})
	require.NoError(t, prometheus.Register(counter))
	t.Cleanup(func() { prometheus.Unregister(counter) })
	counter.Inc()

	// Logs: both the slog default handler and rerouted stdlib log calls.
	slog.InfoContext(context.Background(), "slog test line")
	log.Printf("stdlib log test line")

	require.NoError(t, shutdown(context.Background()))

	assert.NotEmpty(t, recorder.requests("/v1/traces"), "expected trace export")
	assert.NotEmpty(t, recorder.requests("/v1/logs"), "expected log export")

	metricBodies := recorder.requests("/v1/metrics")
	require.NotEmpty(t, metricBodies, "expected metric export")
	found := false
	for _, body := range metricBodies {
		var req collectormetrics.ExportMetricsServiceRequest
		require.NoError(t, proto.Unmarshal(body, &req))
		for _, rm := range req.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if strings.HasPrefix(m.Name, "observability_bridge_test") {
						found = true
					}
				}
			}
		}
	}
	assert.True(t, found, "prometheus metric should be bridged onto OTLP")
}

func TestInfofDisabledKeepsStdlibFormat(t *testing.T) {
	t.Setenv("OTEL_ENABLED", "false")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	Infof(context.Background(), "hello %s", "world")
	Errorf(context.Background(), "bad %s", "thing")

	assert.Contains(t, buf.String(), "hello world")
	assert.Contains(t, buf.String(), "bad thing")
}
