package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	AlertsReceived = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reflex_alerts_received_total",
		Help: "The total number of incoming alerts",
	})
	
	AlertsDeduplicated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reflex_alerts_deduplicated_total",
		Help: "The total number of alerts dropped by Storm-Breaker",
	})
	
	MoALatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "reflex_moa_latency_seconds",
		Help:    "Latency of the Mixture-of-Agents consensus evaluation",
		Buckets: prometheus.DefBuckets,
	})
	
	RemediationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reflex_remediations_total",
		Help: "The total number of DAG remediations executed",
	}, []string{"status"}) // "success", "failed", "rolled_back"
)
