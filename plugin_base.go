package boilerplates

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	amqp_helper "github.com/CodeClarityCE/utility-amqp-helper"
	types_amqp "github.com/CodeClarityCE/utility-types/amqp"
	codeclarity "github.com/CodeClarityCE/utility-types/codeclarity_db"
	plugin_db "github.com/CodeClarityCE/utility-types/plugin_db"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

// PluginDatabases holds all database connections needed by plugins
type PluginDatabases struct {
	Codeclarity *bun.DB
	Knowledge   *bun.DB
	Plugins     *bun.DB
}

// PluginBase provides common functionality for all plugins, eliminating boilerplate code
type PluginBase struct {
	Config      plugin_db.Plugin
	DB          *PluginDatabases
	ConfigSvc   *ConfigService
	Logger      *logrus.Logger
	startTime   time.Time
	healthGauge *prometheus.GaugeVec
}

// AnalysisHandler defines the interface that plugins must implement
type AnalysisHandler interface {
	StartAnalysis(
		databases *PluginDatabases,
		dispatcherMessage types_amqp.DispatcherPluginMessage,
		config plugin_db.Plugin,
		analysisDoc codeclarity.Analysis,
	) (map[string]any, codeclarity.AnalysisStatus, error)
}

// CreatePluginBase creates a new PluginBase with all common setup handled
func CreatePluginBase() (*PluginBase, error) {
	// Initialize configuration service
	configSvc, err := CreateConfigService()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize config service: %w", err)
	}

	// Read plugin configuration
	config, err := readPluginConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to read plugin config: %w", err)
	}

	// Initialize databases
	databases, err := initializeDatabases(configSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize databases: %w", err)
	}

	// Register plugin
	err = registerPlugin(config, databases.Plugins)
	if err != nil {
		log.Printf("Plugin registration failed (non-fatal): %v", err)
		// Don't fail startup for registration issues
	}

	// Initialize structured logger
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.InfoLevel)
	logger.WithField("service", config.Name).Info("Plugin starting")

	// Create health status metric
	healthGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "service_health_status",
			Help: "Health status of the service (1 = healthy, 0 = unhealthy)",
		},
		[]string{"service", "component"},
	)

	// Register the metric (ignore error if already registered)
	prometheus.Register(healthGauge)

	// Set initial health status to healthy
	healthGauge.WithLabelValues(config.Name, "overall").Set(1)

	// Start metrics server
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		logger.WithField("port", 8080).Info("Starting metrics server")
		if err := http.ListenAndServe(":8080", nil); err != nil {
			logger.WithError(err).Error("Failed to start metrics server")
		}
	}()

	return &PluginBase{
		Config:      config,
		DB:          databases,
		ConfigSvc:   configSvc,
		Logger:      logger,
		startTime:   time.Now(),
		healthGauge: healthGauge,
	}, nil
}

// Listen starts listening on the plugin's AMQP queue with automatic message handling
func (pb *PluginBase) Listen(handler AnalysisHandler) error {
	log.Printf("Starting plugin %s (version %s)", pb.Config.Name, pb.Config.Version)

	queueName := "dispatcher_" + pb.Config.Name

	// Create callback wrapper that handles all common plugin logic
	callback := pb.createCallbackWrapper(handler)

	// Start listening (this blocks)
	amqp_helper.Listen(queueName, callback, pb, pb.Config)
	return nil
}

// createCallbackWrapper creates a callback function that handles all common plugin logic
func (pb *PluginBase) createCallbackWrapper(handler AnalysisHandler) func(any, plugin_db.Plugin, []byte) {
	return func(args any, config plugin_db.Plugin, message []byte) {
		start := time.Now()

		// Parse message
		var dispatcherMessage types_amqp.DispatcherPluginMessage
		err := json.Unmarshal(message, &dispatcherMessage)
		if err != nil {
			pb.logError("Failed to unmarshal message", err)
			return
		}

		// Get analysis document
		ctx := context.Background()
		analysisDoc := codeclarity.Analysis{Id: dispatcherMessage.AnalysisId}
		err = pb.DB.Codeclarity.NewSelect().Model(&analysisDoc).WherePK().Scan(ctx)
		if err != nil {
			pb.logError("Failed to retrieve analysis document", err)
			return
		}

		// Recover from a panic in the plugin handler. The plugin queue auto-acks,
		// so a panic would otherwise drop the message with the step left STARTED
		// forever (unreachable by the dispatcher reaper). Record FAILURE + notify
		// so the analysis becomes terminal, mirroring the error path below.
		defer func() {
			if r := recover(); r != nil {
				pb.logError("Analysis panicked", fmt.Errorf("%v", r))
				if _, uerr := pb.updateAnalysisStep(analysisDoc, config, map[string]any{}, codeclarity.FAILURE, start, time.Now()); uerr != nil {
					pb.logError("Failed to record analysis failure after panic", uerr)
				}
				pb.notifyCompletion(dispatcherMessage.AnalysisId.String(), config.Name)
			}
		}()

		// Execute plugin-specific analysis
		result, status, err := handler.StartAnalysis(pb.DB, dispatcherMessage, config, analysisDoc)
		if err != nil {
			pb.logError("Analysis failed", err)
			// Update with failure status, then notify the dispatcher so it can
			// finalize the analysis as FAILURE. Without this notification a failed
			// plugin would leave the analysis stuck non-terminal forever.
			if _, uerr := pb.updateAnalysisStep(analysisDoc, config, map[string]any{}, codeclarity.FAILURE, start, time.Now()); uerr != nil {
				pb.logError("Failed to record analysis failure", uerr)
			}
			pb.notifyCompletion(dispatcherMessage.AnalysisId.String(), config.Name)
			return
		}

		// Update analysis with results
		updatedDoc, err := pb.updateAnalysisStep(analysisDoc, config, result, status, start, time.Now())
		if err != nil {
			pb.logError("Failed to update analysis", err)
			return
		}

		// Log completion time
		elapsed := time.Since(start)
		log.Printf("Plugin %s completed in %v", config.Name, elapsed)

		// Send completion message to dispatcher
		pb.notifyCompletion(dispatcherMessage.AnalysisId.String(), config.Name)

		_ = updatedDoc // Use variable to avoid unused warning
	}
}

// updateAnalysisStep updates the analysis document with step results
func (pb *PluginBase) updateAnalysisStep(
	analysisDoc codeclarity.Analysis,
	config plugin_db.Plugin,
	result map[string]any,
	status codeclarity.AnalysisStatus,
	start, end time.Time,
) (codeclarity.Analysis, error) {

	return pb.updateAnalysisInTransaction(analysisDoc, config, result, status, start, end)
}

// updateAnalysisInTransaction handles the database transaction for analysis updates
func (pb *PluginBase) updateAnalysisInTransaction(
	analysisDoc codeclarity.Analysis,
	config plugin_db.Plugin,
	result map[string]any,
	status codeclarity.AnalysisStatus,
	start, end time.Time,
) (codeclarity.Analysis, error) {

	err := pb.DB.Codeclarity.RunInTx(context.Background(), &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		// Reload analysis document with row lock to prevent lost updates
		// when multiple plugins complete concurrently
		err := tx.NewSelect().Model(&analysisDoc).WherePK().For("UPDATE").Scan(ctx)
		if err != nil {
			return fmt.Errorf("failed to reload analysis: %w", err)
		}

		// Find and update the correct step by name across ALL stages, rather
		// than indexing analysisDoc.Steps[analysisDoc.Stage]. When a stage holds
		// several steps that run concurrently (e.g. vuln-finder + license-finder),
		// the dispatcher can advance analysisDoc.Stage past this plugin's stage —
		// even past the end of Steps — before this slower step's result lands.
		// Indexing by the live Stage then panicked with index-out-of-range,
		// killing the plugin's AMQP consumer and stalling every queued analysis.
		// A plugin appears exactly once across the stages, so matching by name is
		// unambiguous and independent of the volatile Stage pointer.
		stepFound := false
		for stageId := range analysisDoc.Steps {
			for stepId := range analysisDoc.Steps[stageId] {
				if analysisDoc.Steps[stageId][stepId].Name == config.Name {
					analysisDoc.Steps[stageId][stepId].Status = status
					analysisDoc.Steps[stageId][stepId].Result = result
					analysisDoc.Steps[stageId][stepId].Started_on = start.Format(time.RFC3339Nano)
					analysisDoc.Steps[stageId][stepId].Ended_on = end.Format(time.RFC3339Nano)
					stepFound = true
					break
				}
			}
			if stepFound {
				break
			}
		}

		if !stepFound {
			return fmt.Errorf("step %s not found in any of %d stage(s)", config.Name, len(analysisDoc.Steps))
		}

		// Save updated analysis
		_, err = tx.NewUpdate().Model(&analysisDoc).WherePK().Exec(ctx)
		return err
	})

	if err != nil {
		return codeclarity.Analysis{}, fmt.Errorf("transaction failed: %w", err)
	}

	return analysisDoc, nil
}

// notifyCompletion sends completion notification to dispatcher
func (pb *PluginBase) notifyCompletion(analysisId string, pluginName string) {
	// Parse analysisId back to UUID for the message
	analysisUUID, err := uuid.Parse(analysisId)
	if err != nil {
		pb.logError("Failed to parse analysis ID", err)
		return
	}

	message := types_amqp.PluginDispatcherMessage{
		AnalysisId: analysisUUID,
		Plugin:     pluginName,
	}

	data, err := json.Marshal(message)
	if err != nil {
		pb.logError("Failed to marshal completion message", err)
		return
	}

	// NOTE: amqp_helper.Send does not surface publish errors. A lost completion
	// notification leaves the analysis non-terminal until the dispatcher's reaper
	// reconciles it (see reaper.go), which is the recovery path for this gap.
	amqp_helper.Send("plugins_dispatcher", data)
}

// logError logs errors with plugin context
func (pb *PluginBase) logError(message string, err error) {
	log.Printf("[%s] %s: %v", pb.Config.Name, message, err)
}

// Close cleanly shuts down the plugin base and closes all connections
func (pb *PluginBase) Close() error {
	var errors []error

	if pb.DB.Codeclarity != nil {
		if err := pb.DB.Codeclarity.Close(); err != nil {
			errors = append(errors, fmt.Errorf("codeclarity db close: %w", err))
		}
	}

	if pb.DB.Knowledge != nil {
		if err := pb.DB.Knowledge.Close(); err != nil {
			errors = append(errors, fmt.Errorf("knowledge db close: %w", err))
		}
	}

	if pb.DB.Plugins != nil {
		if err := pb.DB.Plugins.Close(); err != nil {
			errors = append(errors, fmt.Errorf("plugins db close: %w", err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("close errors: %v", errors)
	}

	return nil
}

// Helper functions (extracted from original boilerplate)

// readPluginConfig reads the plugin configuration from config.json
func readPluginConfig() (plugin_db.Plugin, error) {
	var config plugin_db.Plugin

	configFile, err := openConfigFile()
	if err != nil {
		return config, err
	}
	defer configFile.Close()

	decoder := json.NewDecoder(configFile)
	err = decoder.Decode(&config)
	if err != nil {
		return config, fmt.Errorf("failed to decode config: %w", err)
	}

	return config, nil
}

// initializeDatabases creates all required database connections
func initializeDatabases(configSvc *ConfigService) (*PluginDatabases, error) {
	// Create codeclarity database connection
	codeclarity, err := createDatabaseConnection(configSvc.GetDatabaseDSN("results"), &configSvc.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to codeclarity db: %w", err)
	}

	// Create knowledge database connection
	knowledge, err := createDatabaseConnection(configSvc.GetDatabaseDSN("knowledge"), &configSvc.Database)
	if err != nil {
		codeclarity.Close()
		return nil, fmt.Errorf("failed to connect to knowledge db: %w", err)
	}

	// Create plugins database connection
	plugins, err := createDatabaseConnection(configSvc.GetDatabaseDSN("plugins"), &configSvc.Database)
	if err != nil {
		codeclarity.Close()
		knowledge.Close()
		return nil, fmt.Errorf("failed to connect to plugins db: %w", err)
	}

	return &PluginDatabases{
		Codeclarity: codeclarity,
		Knowledge:   knowledge,
		Plugins:     plugins,
	}, nil
}

// buildTLSOption returns the pgdriver TLS option for the given database config.
// This explicitly configures TLS rather than relying on pgdriver's DSN parsing,
// which can silently fall back to non-TLS on cert loading errors.
func buildTLSOption(dbConfig *DatabaseConfig) pgdriver.Option {
	switch dbConfig.SSLMode {
	case "require":
		return pgdriver.WithTLSConfig(&tls.Config{
			InsecureSkipVerify: true,
		})
	case "verify-ca", "verify-full":
		tlsConfig := &tls.Config{
			ServerName: dbConfig.Host,
		}
		if dbConfig.SSLRootCert != "" {
			if caCert, err := os.ReadFile(dbConfig.SSLRootCert); err == nil {
				pool := x509.NewCertPool()
				pool.AppendCertsFromPEM(caCert)
				tlsConfig.RootCAs = pool
			}
		}
		return pgdriver.WithTLSConfig(tlsConfig)
	default:
		return pgdriver.WithInsecure(true)
	}
}

// createDatabaseConnection creates a new database connection with standard settings
func createDatabaseConnection(dsn string, dbConfig *DatabaseConfig) (*bun.DB, error) {
	sqldb := sql.OpenDB(pgdriver.NewConnector(
		pgdriver.WithDSN(dsn),
		pgdriver.WithTimeout(dbConfig.Timeout),
		buildTLSOption(dbConfig),
	))

	// Bound the connection pool (env-overridable via DB_MAX_OPEN_CONNS etc.).
	// Without this, plugin pools were unbounded and exhausted Postgres under
	// high replica fan-out.
	dbConfig.ApplyPool(sqldb)

	db := bun.NewDB(sqldb, pgdialect.New())

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("database ping failed: %w", err)
	}

	return db, nil
}

// registerPlugin registers the plugin in the plugins database
func registerPlugin(config plugin_db.Plugin, db *bun.DB) error {
	ctx := context.Background()

	exists, err := db.NewSelect().
		Model((*plugin_db.Plugin)(nil)).
		Where("name = ?", config.Name).
		Exists(ctx)

	if err != nil {
		return fmt.Errorf("failed to check plugin existence: %w", err)
	}

	if !exists {
		_, err = db.NewInsert().Model(&config).Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to register plugin: %w", err)
		}
		log.Printf("Plugin %s registered successfully", config.Name)
	} else {
		log.Printf("Plugin %s already registered", config.Name)
	}

	return nil
}

// openConfigFile opens the config.json file with better error handling
func openConfigFile() (*os.File, error) {
	file, err := os.Open("config.json")
	if err != nil {
		return nil, fmt.Errorf("failed to open config.json: %w", err)
	}
	return file, nil
}
