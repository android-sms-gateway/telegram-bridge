package webhook

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	metricsNamespace = "bridge"
	metricsSubsystem = "webhook"

	resultLabel = "result"

	resultOK       = "ok"
	resultRejected = "rejected"
	resultError    = "error"
)

// Metrics collects Prometheus metrics for webhook processing.
type Metrics struct {
	// total counts webhook requests by result (ok, rejected, error).
	total *prometheus.CounterVec

	// forwardErrors counts failures to deliver a message to Telegram.
	forwardErrors prometheus.Counter
}

// NewMetrics creates and registers the webhook metrics in the default
// registry.
func NewMetrics() *Metrics {
	return NewMetricsWithRegisterer(prometheus.DefaultRegisterer)
}

// NewMetricsWithRegisterer creates and registers the webhook metrics in the
// given registerer. Used by tests to isolate metric state.
func NewMetricsWithRegisterer(reg prometheus.Registerer) *Metrics {
	return &Metrics{
		total: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "total",
			Help:      "Total number of processed webhook requests",
		}, []string{resultLabel}),
		forwardErrors: promauto.With(reg).NewCounter(prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "forward_errors_total",
			Help:      "Total number of failed forwards to Telegram",
		}),
	}
}

// IncTotal increments the total counter with the given result label.
func (m *Metrics) IncTotal(result string) {
	m.total.WithLabelValues(result).Inc()
}

// IncForwardErrors increments the forward errors counter.
func (m *Metrics) IncForwardErrors() {
	m.forwardErrors.Inc()
}
