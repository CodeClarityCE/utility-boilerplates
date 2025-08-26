package boilerplates

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

// ServiceMetrics holds all Prometheus metrics for a service
type ServiceMetrics struct {
	// Service health metrics
	ServiceHealthStatus *prometheus.GaugeVec
	ServiceUptime       prometheus.Gauge
	ServiceRestarts     prometheus.Counter

	// Database metrics
	DatabaseConnections   *prometheus.GaugeVec
	DatabaseOperations    *prometheus.CounterVec
	DatabaseQueryDuration *prometheus.HistogramVec
	DatabaseHealthChecks  *prometheus.CounterVec

	// AMQP metrics
	AMQPConnections       *prometheus.GaugeVec
	MessagesProcessed     *prometheus.CounterVec
	MessageProcessingTime *prometheus.HistogramVec
	QueueConsumers        *prometheus.GaugeVec

	// Service-specific metrics
	startTime time.Time
}

// CreateServiceMetrics creates and registers Prometheus metrics for a service
func CreateServiceMetrics(serviceName string) *ServiceMetrics {
	metrics := &ServiceMetrics{
		startTime: time.Now(),

		// Service health metrics
		ServiceHealthStatus: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "service_health_status",
				Help: "Current health status of the service (1=healthy, 0=unhealthy)",
			},
			[]string{"service", "component"},
		),

		ServiceUptime: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "service_uptime_seconds",
				Help: "Service uptime in seconds",
			},
		),

		ServiceRestarts: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "service_restarts_total",
				Help: "Total number of service restarts",
			},
		),

		// Database metrics
		DatabaseConnections: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "database_connections_active",
				Help: "Number of active database connections",
			},
			[]string{"database", "state"},
		),

		DatabaseOperations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "database_operations_total",
				Help: "Total number of database operations",
			},
			[]string{"database", "operation", "status"},
		),

		DatabaseQueryDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "database_query_duration_seconds",
				Help:    "Database query execution time in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"database", "operation"},
		),

		DatabaseHealthChecks: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "database_health_checks_total",
				Help: "Total number of database health checks",
			},
			[]string{"database", "status"},
		),

		// AMQP metrics
		AMQPConnections: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "amqp_connections_active",
				Help: "Number of active AMQP connections",
			},
			[]string{"connection_type"},
		),

		MessagesProcessed: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "amqp_messages_processed_total",
				Help: "Total number of AMQP messages processed",
			},
			[]string{"queue", "status"},
		),

		MessageProcessingTime: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "amqp_message_processing_duration_seconds",
				Help:    "Time spent processing AMQP messages in seconds",
				Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0},
			},
			[]string{"queue"},
		),

		QueueConsumers: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "amqp_queue_consumers",
				Help: "Number of active consumers per queue",
			},
			[]string{"queue"},
		),
	}

	// Register all metrics with Prometheus, handling potential re-registration errors
	metricsToRegister := []prometheus.Collector{
		metrics.ServiceHealthStatus,
		metrics.ServiceUptime,
		metrics.ServiceRestarts,
		metrics.DatabaseConnections,
		metrics.DatabaseOperations,
		metrics.DatabaseQueryDuration,
		metrics.DatabaseHealthChecks,
		metrics.AMQPConnections,
		metrics.MessagesProcessed,
		metrics.MessageProcessingTime,
		metrics.QueueConsumers,
	}

	for _, metric := range metricsToRegister {
		if err := prometheus.Register(metric); err != nil {
			// Ignore errors for already registered metrics
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				logrus.WithError(err).Warn("Failed to register metric")
			}
		}
	}

	// Set initial values
	metrics.ServiceHealthStatus.WithLabelValues(serviceName, "overall").Set(1)
	metrics.AMQPConnections.WithLabelValues("primary").Set(0)

	return metrics
}

// UpdateUptime updates the service uptime metric
func (m *ServiceMetrics) UpdateUptime() {
	m.ServiceUptime.Set(time.Since(m.startTime).Seconds())
}

// RecordRestart increments the restart counter
func (m *ServiceMetrics) RecordRestart() {
	m.ServiceRestarts.Inc()
}

// SetHealthStatus sets the health status for a component
func (m *ServiceMetrics) SetHealthStatus(serviceName, component string, healthy bool) {
	value := 0.0
	if healthy {
		value = 1.0
	}
	m.ServiceHealthStatus.WithLabelValues(serviceName, component).Set(value)
}

// RecordDatabaseOperation records a database operation
func (m *ServiceMetrics) RecordDatabaseOperation(database, operation, status string, duration time.Duration) {
	m.DatabaseOperations.WithLabelValues(database, operation, status).Inc()
	m.DatabaseQueryDuration.WithLabelValues(database, operation).Observe(duration.Seconds())
}

// RecordDatabaseHealthCheck records a database health check
func (m *ServiceMetrics) RecordDatabaseHealthCheck(database, status string) {
	m.DatabaseHealthChecks.WithLabelValues(database, status).Inc()
}

// RecordMessageProcessed records an AMQP message processing event
func (m *ServiceMetrics) RecordMessageProcessed(queue, status string, duration time.Duration) {
	m.MessagesProcessed.WithLabelValues(queue, status).Inc()
	m.MessageProcessingTime.WithLabelValues(queue).Observe(duration.Seconds())
}

// SetQueueConsumers sets the number of consumers for a queue
func (m *ServiceMetrics) SetQueueConsumers(queue string, count int) {
	m.QueueConsumers.WithLabelValues(queue).Set(float64(count))
}

// SetAMQPConnections sets the number of AMQP connections
func (m *ServiceMetrics) SetAMQPConnections(connectionType string, count int) {
	m.AMQPConnections.WithLabelValues(connectionType).Set(float64(count))
}

var metricsServerStarted bool

// StartMetricsServer starts the Prometheus metrics HTTP server
func StartMetricsServer(port string) {
	if metricsServerStarted {
		logrus.WithField("port", port).Debug("Metrics server already started, skipping")
		return
	}

	logrus.WithField("port", port).Info("Starting Prometheus metrics server")

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	// Add health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	metricsServerStarted = true

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.WithError(err).Error("Metrics server failed")
			metricsServerStarted = false
		}
	}()
}
