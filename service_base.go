package boilerplates

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	amqp_helper "github.com/CodeClarityCE/utility-amqp-helper"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

type ServiceDatabases struct {
	CodeClarity *bun.DB
	Knowledge   *bun.DB
	Plugins     *bun.DB
	Config      *bun.DB
}

type QueueConfig struct {
	Name    string
	Durable bool
	Handler func(d amqp.Delivery)
}

type ServiceBase struct {
	ConfigSvc      *ConfigService
	DB             *ServiceDatabases
	conn           *amqp.Connection
	channels       map[string]*amqp.Channel
	declaredQueues map[string]bool
	queues         []QueueConfig
	Metrics        *ServiceMetrics
	ServiceName    string
	Logger         *logrus.Logger
}

func CreateServiceBase() (*ServiceBase, error) {
	// Read service name from config.json
	config, err := readServiceConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to read service config: %w", err)
	}

	return CreateServiceBaseWithName(config.Name)
}

func CreateServiceBaseWithName(serviceName string) (*ServiceBase, error) {
	configSvc, err := CreateConfigService()
	if err != nil {
		return nil, fmt.Errorf("config service init failed: %w", err)
	}

	db, err := connectServiceDatabases(configSvc)
	if err != nil {
		return nil, fmt.Errorf("database connection failed: %w", err)
	}

	conn, err := connectAMQP(configSvc)
	if err != nil {
		return nil, fmt.Errorf("AMQP connection failed: %w", err)
	}

	// Initialize metrics
	metrics := CreateServiceMetrics(serviceName)

	// Start metrics server
	StartMetricsServer("8080")

	// Initialize structured logger
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339,
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime:  "timestamp",
			logrus.FieldKeyLevel: "level",
			logrus.FieldKeyMsg:   "message",
		},
	})

	// Set log level from environment or default to INFO
	logLevel := os.Getenv("LOG_LEVEL")
	switch logLevel {
	case "DEBUG":
		logger.SetLevel(logrus.DebugLevel)
	case "WARN":
		logger.SetLevel(logrus.WarnLevel)
	case "ERROR":
		logger.SetLevel(logrus.ErrorLevel)
	default:
		logger.SetLevel(logrus.InfoLevel)
	}

	sb := &ServiceBase{
		ConfigSvc:      configSvc,
		DB:             db,
		conn:           conn,
		channels:       make(map[string]*amqp.Channel),
		declaredQueues: make(map[string]bool),
		queues:         make([]QueueConfig, 0),
		Metrics:        metrics,
		ServiceName:    serviceName,
		Logger:         logger,
	}

	// Set initial health status to healthy
	sb.Metrics.ServiceHealthStatus.WithLabelValues(serviceName, "overall").Set(1)

	return sb, nil
}

func (sb *ServiceBase) updateMetricsPeriodically() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		sb.Metrics.UpdateUptime()
		sb.updateConnectionMetrics()
	}
}

func (sb *ServiceBase) updateConnectionMetrics() {
	// Update AMQP connection status
	if sb.conn != nil && !sb.conn.IsClosed() {
		sb.Metrics.SetAMQPConnections("primary", 1)
	} else {
		sb.Metrics.SetAMQPConnections("primary", 0)
	}

	// Update queue consumer counts
	for _, queue := range sb.queues {
		sb.Metrics.SetQueueConsumers(queue.Name, 1)
	}

	// Update database connection metrics
	if sb.DB != nil {
		if sb.DB.CodeClarity != nil {
			stats := sb.DB.CodeClarity.DB.Stats()
			sb.Metrics.DatabaseConnections.WithLabelValues("codeclarity", "open").Set(float64(stats.OpenConnections))
			sb.Metrics.DatabaseConnections.WithLabelValues("codeclarity", "idle").Set(float64(stats.Idle))
			sb.Metrics.DatabaseConnections.WithLabelValues("codeclarity", "in_use").Set(float64(stats.InUse))
		}
		if sb.DB.Knowledge != nil {
			stats := sb.DB.Knowledge.DB.Stats()
			sb.Metrics.DatabaseConnections.WithLabelValues("knowledge", "open").Set(float64(stats.OpenConnections))
			sb.Metrics.DatabaseConnections.WithLabelValues("knowledge", "idle").Set(float64(stats.Idle))
			sb.Metrics.DatabaseConnections.WithLabelValues("knowledge", "in_use").Set(float64(stats.InUse))
		}
		if sb.DB.Config != nil {
			stats := sb.DB.Config.DB.Stats()
			sb.Metrics.DatabaseConnections.WithLabelValues("config", "open").Set(float64(stats.OpenConnections))
			sb.Metrics.DatabaseConnections.WithLabelValues("config", "idle").Set(float64(stats.Idle))
			sb.Metrics.DatabaseConnections.WithLabelValues("config", "in_use").Set(float64(stats.InUse))
		}
		if sb.DB.Plugins != nil {
			stats := sb.DB.Plugins.DB.Stats()
			sb.Metrics.DatabaseConnections.WithLabelValues("plugins", "open").Set(float64(stats.OpenConnections))
			sb.Metrics.DatabaseConnections.WithLabelValues("plugins", "idle").Set(float64(stats.Idle))
			sb.Metrics.DatabaseConnections.WithLabelValues("plugins", "in_use").Set(float64(stats.InUse))
		}
	}
}

func connectServiceDatabases(configSvc *ConfigService) (*ServiceDatabases, error) {
	tlsOpt := buildTLSOption(&configSvc.Database)

	// CodeClarity Database (using sql.DB for compatibility)
	codeClarityDSN := configSvc.GetDatabaseDSN("results")
	codeClaritySqlDB := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(codeClarityDSN), pgdriver.WithTimeout(30*time.Second), tlsOpt))

	// Bound the connection pool (env-overridable via DB_MAX_OPEN_CONNS etc.)
	configSvc.Database.ApplyPool(codeClaritySqlDB)

	if err := codeClaritySqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("codeclarity database ping failed: %w", err)
	}

	// Knowledge Database (using bun.DB)
	knowledgeDSN := configSvc.GetDatabaseDSN("knowledge")
	knowledgeSqlDB := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(knowledgeDSN), pgdriver.WithTimeout(30*time.Second), tlsOpt))

	configSvc.Database.ApplyPool(knowledgeSqlDB)

	knowledgeDB := bun.NewDB(knowledgeSqlDB, pgdialect.New())

	// Plugins Database (using bun.DB)
	pluginsDSN := configSvc.GetDatabaseDSN("plugins")
	pluginsSqlDB := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(pluginsDSN), pgdriver.WithTimeout(30*time.Second), tlsOpt))

	configSvc.Database.ApplyPool(pluginsSqlDB)

	pluginsDB := bun.NewDB(pluginsSqlDB, pgdialect.New())

	// Config Database (using bun.DB)
	configDSN := configSvc.GetDatabaseDSN("config")
	configSqlDB := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(configDSN), pgdriver.WithTimeout(30*time.Second), tlsOpt))

	configSvc.Database.ApplyPool(configSqlDB)

	configDB := bun.NewDB(configSqlDB, pgdialect.New())

	codeClarityDB := bun.NewDB(codeClaritySqlDB, pgdialect.New())

	return &ServiceDatabases{
		CodeClarity: codeClarityDB,
		Knowledge:   knowledgeDB,
		Plugins:     pluginsDB,
		Config:      configDB,
	}, nil
}

// amqpConnectMaxWait bounds how long connectAMQP retries before giving up.
// Overridable via AMQP_CONNECT_MAX_WAIT_SECONDS; defaults to 120s.
func amqpConnectMaxWait() time.Duration {
	const def = 120 * time.Second
	v := os.Getenv("AMQP_CONNECT_MAX_WAIT_SECONDS")
	if v == "" {
		return def
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		log.Printf("[amqp] invalid AMQP_CONNECT_MAX_WAIT_SECONDS=%q, using default %s", v, def)
		return def
	}
	return time.Duration(secs) * time.Second
}

func connectAMQP(configSvc *ConfigService) (*amqp.Connection, error) {
	// Use pre-constructed URL from config service. Dial transparently negotiates
	// TLS when the URL scheme is "amqps" (driven by AMQP_SSLMODE/AMQP_SSLROOTCERT).
	//
	// Retry with capped backoff rather than failing fast: a service often starts
	// before RabbitMQ's listener is accepting (the depends_on healthcheck can pass
	// before the AMQP/TLS port is ready) or while RabbitMQ is restarting (e.g. on a
	// `make up` cycle, where its container IP changes). Without this, the process
	// exits and the supervisor (air/docker restart:always) crash-loops, and if every
	// retry lands inside RabbitMQ's downtime window the service never attaches —
	// leaving queues with zero consumers. Runtime drops are handled separately by
	// monitorConnection/restart.
	deadline := time.Now().Add(amqpConnectMaxWait())
	backoff := 500 * time.Millisecond
	const maxBackoff = 5 * time.Second
	var lastErr error
	for attempt := 1; ; attempt++ {
		conn, err := amqp_helper.Dial(configSvc.AMQP.URL)
		if err == nil {
			if attempt > 1 {
				log.Printf("[amqp] connected to RabbitMQ after %d attempt(s)", attempt)
			}
			return conn, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		log.Printf("[amqp] connect attempt %d failed (%v); retrying in %s", attempt, err, backoff)
		time.Sleep(backoff)
		if backoff < maxBackoff {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
	return nil, fmt.Errorf("failed to connect to RabbitMQ after %s: %w", amqpConnectMaxWait(), lastErr)
}

func (sb *ServiceBase) AddQueue(name string, durable bool, handler func(d amqp.Delivery)) {
	sb.queues = append(sb.queues, QueueConfig{
		Name:    name,
		Durable: durable,
		Handler: handler,
	})
}

func (sb *ServiceBase) StartListening() error {
	for _, queueConfig := range sb.queues {
		if err := sb.startQueueListener(queueConfig); err != nil {
			return fmt.Errorf("failed to start listener for queue %s: %w", queueConfig.Name, err)
		}
		log.Printf("Started listening on queue: %s", queueConfig.Name)
	}
	return nil
}

func (sb *ServiceBase) startQueueListener(config QueueConfig) error {
	ch, err := sb.conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open channel: %w", err)
	}

	// Optimize channel performance with prefetch settings
	err = ch.Qos(
		10,    // prefetch count - process up to 10 messages concurrently
		0,     // prefetch size - 0 means no limit on message size
		false, // global - apply per consumer
	)
	if err != nil {
		ch.Close()
		return fmt.Errorf("failed to set QoS: %w", err)
	}

	sb.channels[config.Name] = ch

	q, err := ch.QueueDeclare(
		config.Name,    // name
		config.Durable, // durable
		false,          // delete when unused
		false,          // exclusive
		false,          // no-wait
		nil,            // arguments - remove queue limits for compatibility
	)
	if err != nil {
		ch.Close()
		return fmt.Errorf("failed to declare queue: %w", err)
	}

	msgs, err := ch.Consume(
		q.Name, // queue
		"",     // consumer
		false,  // auto-ack - changed to manual ack for reliability
		false,  // exclusive
		false,  // no-local
		false,  // no-wait
		nil,    // args
	)
	if err != nil {
		return fmt.Errorf("failed to register consumer: %w", err)
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Queue %s handler panicked: %v", config.Name, r)
			}
		}()

		for d := range msgs {
			start := time.Now()

			// Process message with error handling and metrics
			func() {
				defer func() {
					if r := recover(); r != nil {
						duration := time.Since(start)
						logrus.WithFields(logrus.Fields{
							"queue":      config.Name,
							"error":      r,
							"duration":   duration,
							"redelivered": d.Redelivered,
						}).Error("Queue message handler panicked")
						sb.Metrics.RecordMessageProcessed(config.Name, "panic", duration)
						// Requeue once, then drop. A message that deterministically
						// panics (poison message) would otherwise be redelivered
						// forever, monopolizing this queue's single consumer and
						// starving every other message. The redelivery flag lets us
						// give a transient failure one retry without looping.
						if d.Redelivered {
							log.Printf("Queue %s: dropping poison message after redelivery: %v", config.Name, r)
							d.Nack(false, false) // drop (dead-letter if a DLX is configured)
						} else {
							d.Nack(false, true) // first failure: requeue once
						}
						return
					}
				}()

				config.Handler(d)
				duration := time.Since(start)
				d.Ack(false) // Acknowledge successful processing
				sb.Metrics.RecordMessageProcessed(config.Name, "success", duration)
				logrus.WithFields(logrus.Fields{
					"queue":    config.Name,
					"duration": duration,
				}).Debug("Queue processed message")
			}()
		}

		log.Printf("Queue %s consumer stopped", config.Name)
	}()

	return nil
}

func (sb *ServiceBase) SendMessage(queueName string, data []byte) error {
	ch, exists := sb.channels[queueName]
	if !exists {
		// Create new channel for sending if it doesn't exist
		var err error
		ch, err = sb.conn.Channel()
		if err != nil {
			return fmt.Errorf("failed to open channel for sending: %w", err)
		}
		sb.channels[queueName] = ch
	}

	// Only declare queue if not already declared on this channel
	if !sb.declaredQueues[queueName] {
		_, err := ch.QueueDeclare(
			queueName, // name
			true,      // durable
			false,     // delete when unused
			false,     // exclusive
			false,     // no-wait
			nil,       // arguments
		)
		if err != nil {
			// Invalidate channel cache on error
			delete(sb.channels, queueName)
			delete(sb.declaredQueues, queueName)
			return fmt.Errorf("failed to declare queue for sending: %w", err)
		}
		sb.declaredQueues[queueName] = true
	}

	err := ch.Publish(
		"",        // exchange
		queueName, // routing key
		false,     // mandatory
		false,     // immediate
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        data,
		})
	if err != nil {
		// Invalidate channel cache on publish error
		delete(sb.channels, queueName)
		delete(sb.declaredQueues, queueName)
		return fmt.Errorf("failed to publish message: %w", err)
	}

	return nil
}

func (sb *ServiceBase) WaitForever() {
	// Start metrics update goroutine
	go sb.updateMetricsPeriodically()

	for {
		// Set up signal handling for graceful shutdown
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

		// Monitor connection health
		connClosed := make(chan bool, 1)
		go sb.monitorConnection(connClosed)

		// Wait for shutdown signal or connection failure
		select {
		case sig := <-sigChan:
			log.Printf("Received signal %v, shutting down...", sig)
			if err := sb.Close(); err != nil {
				log.Printf("Error during shutdown: %v", err)
			}
			return // Exit completely on manual shutdown
		case <-connClosed:
			logrus.Warning("Connection lost, attempting restart in 10 seconds...")
			sb.Metrics.SetHealthStatus(sb.ServiceName, "overall", false)
			if err := sb.Close(); err != nil {
				logrus.WithError(err).Error("Error during shutdown")
			}

			// Wait before attempting restart
			time.Sleep(10 * time.Second)

			// Attempt to reconnect
			if err := sb.restart(); err != nil {
				logrus.WithError(err).Error("Restart failed, will retry in 30 seconds...")
				time.Sleep(30 * time.Second)
				continue // Retry the restart loop
			}

			sb.Metrics.RecordRestart()
			sb.Metrics.SetHealthStatus(sb.ServiceName, "overall", true)
			logrus.Info("Service restarted successfully")
			// Continue the loop to monitor the new connections
		}
	}
}

// restart attempts to reinitialize all connections
func (sb *ServiceBase) restart() error {
	log.Printf("Attempting to restart service connections...")

	// Reinitialize config service
	configSvc, err := CreateConfigService()
	if err != nil {
		return fmt.Errorf("config service restart failed: %w", err)
	}
	sb.ConfigSvc = configSvc

	// Reinitialize databases
	db, err := connectServiceDatabases(configSvc)
	if err != nil {
		return fmt.Errorf("database restart failed: %w", err)
	}
	sb.DB = db

	// Reinitialize AMQP connection
	conn, err := connectAMQP(configSvc)
	if err != nil {
		return fmt.Errorf("AMQP restart failed: %w", err)
	}
	sb.conn = conn

	// Clear old channels and queue declaration cache
	sb.channels = make(map[string]*amqp.Channel)
	sb.declaredQueues = make(map[string]bool)

	// Restart queue listeners
	if err := sb.StartListening(); err != nil {
		return fmt.Errorf("queue listener restart failed: %w", err)
	}

	return nil
}

func (sb *ServiceBase) monitorConnection(closeChan chan bool) {
	defer close(closeChan)

	// Monitor AMQP connection
	notifyClose := make(chan *amqp.Error, 1)
	if sb.conn != nil {
		sb.conn.NotifyClose(notifyClose)
	}

	// Monitor database connections with periodic health checks
	dbHealthTicker := time.NewTicker(30 * time.Second)
	defer dbHealthTicker.Stop()

	for {
		select {
		case err := <-notifyClose:
			if err != nil {
				log.Printf("AMQP connection lost: %v", err)
				closeChan <- true
				return
			}
			log.Printf("AMQP connection closed gracefully")
			return

		case <-dbHealthTicker.C:
			// Check database health
			if sb.DB != nil {
				if err := sb.checkDatabaseHealth(); err != nil {
					log.Printf("Database health check failed: %v", err)
					closeChan <- true
					return
				}
			}
		}
	}
}

func (sb *ServiceBase) checkDatabaseHealth() error {
	// Check each database connection with timeout
	timeout := 5 * time.Second

	if sb.DB.CodeClarity != nil {
		start := time.Now()
		done := make(chan error, 1)
		go func() {
			done <- sb.DB.CodeClarity.DB.Ping()
		}()

		select {
		case err := <-done:
			duration := time.Since(start)
			if err != nil {
				sb.Metrics.RecordDatabaseHealthCheck("codeclarity", "failed")
				sb.Metrics.SetHealthStatus(sb.ServiceName, "database_codeclarity", false)
				return fmt.Errorf("CodeClarity DB ping failed: %w", err)
			}
			sb.Metrics.RecordDatabaseHealthCheck("codeclarity", "success")
			sb.Metrics.RecordDatabaseOperation("codeclarity", "ping", "success", duration)
			sb.Metrics.SetHealthStatus(sb.ServiceName, "database_codeclarity", true)
		case <-time.After(timeout):
			sb.Metrics.RecordDatabaseHealthCheck("codeclarity", "timeout")
			sb.Metrics.SetHealthStatus(sb.ServiceName, "database_codeclarity", false)
			return fmt.Errorf("CodeClarity DB ping timeout")
		}
	}

	if sb.DB.Knowledge != nil {
		done := make(chan error, 1)
		go func() {
			done <- sb.DB.Knowledge.DB.Ping()
		}()

		select {
		case err := <-done:
			if err != nil {
				return fmt.Errorf("knowledge DB ping failed: %w", err)
			}
		case <-time.After(timeout):
			return fmt.Errorf("knowledge DB ping timeout")
		}
	}

	if sb.DB.Plugins != nil {
		done := make(chan error, 1)
		go func() {
			done <- sb.DB.Plugins.DB.Ping()
		}()

		select {
		case err := <-done:
			if err != nil {
				return fmt.Errorf("plugins DB ping failed: %w", err)
			}
		case <-time.After(timeout):
			return fmt.Errorf("plugins DB ping timeout")
		}
	}

	if sb.DB.Config != nil {
		done := make(chan error, 1)
		go func() {
			done <- sb.DB.Config.DB.Ping()
		}()

		select {
		case err := <-done:
			if err != nil {
				return fmt.Errorf("config DB ping failed: %w", err)
			}
		case <-time.After(timeout):
			return fmt.Errorf("config DB ping timeout")
		}
	}

	return nil
}

// GetHealthStatus returns the current health status of the service
func (sb *ServiceBase) GetHealthStatus() map[string]any {
	status := map[string]any{
		"healthy":   true,
		"timestamp": time.Now().Unix(),
		"database":  map[string]bool{},
		"amqp":      true,
	}

	// Check database health
	if err := sb.checkDatabaseHealth(); err != nil {
		status["healthy"] = false
		status["database_error"] = err.Error()
	}

	// Check AMQP health
	if sb.conn == nil || sb.conn.IsClosed() {
		status["healthy"] = false
		status["amqp"] = false
		status["amqp_error"] = "connection closed"
	}

	return status
}

func (sb *ServiceBase) Close() error {
	for _, ch := range sb.channels {
		if err := ch.Close(); err != nil {
			log.Printf("Error closing channel: %v", err)
		}
	}

	if sb.conn != nil {
		if err := sb.conn.Close(); err != nil {
			log.Printf("Error closing AMQP connection: %v", err)
		}
	}

	if sb.DB != nil {
		if sb.DB.CodeClarity != nil {
			if err := sb.DB.CodeClarity.Close(); err != nil {
				log.Printf("Error closing CodeClarity database: %v", err)
			}
		}
		if sb.DB.Knowledge != nil {
			if err := sb.DB.Knowledge.Close(); err != nil {
				log.Printf("Error closing Knowledge database: %v", err)
			}
		}
		if sb.DB.Plugins != nil {
			if err := sb.DB.Plugins.Close(); err != nil {
				log.Printf("Error closing Plugins database: %v", err)
			}
		}
		if sb.DB.Config != nil {
			if err := sb.DB.Config.Close(); err != nil {
				log.Printf("Error closing Config database: %v", err)
			}
		}
	}

	return nil
}

// Structured logging convenience methods
func (sb *ServiceBase) LogInfo(message string, fields logrus.Fields) {
	sb.Logger.WithFields(fields).WithField("service", sb.ServiceName).Info(message)
}

func (sb *ServiceBase) LogError(message string, err error, fields logrus.Fields) {
	if fields == nil {
		fields = logrus.Fields{}
	}
	fields["error"] = err.Error()
	sb.Logger.WithFields(fields).WithField("service", sb.ServiceName).Error(message)
}

func (sb *ServiceBase) LogWarn(message string, fields logrus.Fields) {
	sb.Logger.WithFields(fields).WithField("service", sb.ServiceName).Warn(message)
}

func (sb *ServiceBase) LogDebug(message string, fields logrus.Fields) {
	sb.Logger.WithFields(fields).WithField("service", sb.ServiceName).Debug(message)
}

// ServiceConfig represents the service configuration from config.json
type ServiceConfig struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	ImageName string `json:"image_name"`
}

// readServiceConfig reads the service configuration from config.json
func readServiceConfig() (ServiceConfig, error) {
	var config ServiceConfig

	configFile, err := os.Open("config.json")
	if err != nil {
		return config, fmt.Errorf("failed to open config.json: %w", err)
	}
	defer configFile.Close()

	decoder := json.NewDecoder(configFile)
	err = decoder.Decode(&config)
	if err != nil {
		return config, fmt.Errorf("failed to decode config: %w", err)
	}

	return config, nil
}
