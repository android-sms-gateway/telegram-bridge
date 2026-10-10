package webhook

import "github.com/prometheus/client_golang/prometheus"

// TotalCounter exposes the total requests counter for test assertions.
func (m *Metrics) TotalCounter() *prometheus.CounterVec {
	return m.total
}

// ForwardErrorsCounter exposes the forward errors counter for test
// assertions.
func (m *Metrics) ForwardErrorsCounter() prometheus.Counter {
	return m.forwardErrors
}
