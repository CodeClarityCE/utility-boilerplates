package boilerplates

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	dbhelper "github.com/CodeClarityCE/utility-dbhelper/helper"
)

// ConfigService centralizes all environment variable management and configuration
type ConfigService struct {
	Database DatabaseConfig `json:"database"`
	AMQP     AMQPConfig     `json:"amqp"`
	General  GeneralConfig  `json:"general"`
}

// DatabaseConfig holds database connection configuration
type DatabaseConfig struct {
	Host        string        `json:"host"`
	Port        string        `json:"port"`
	User        string        `json:"user"`
	Password    string        `json:"password"`
	SSLMode     string        `json:"sslMode"`
	SSLRootCert string        `json:"sslRootCert"`
	SSLCert     string        `json:"sslCert"`
	SSLKey      string        `json:"sslKey"`
	Timeout     time.Duration `json:"timeout"`

	// Connection pool settings (env-overridable). Bound the per-instance
	// connection footprint so replicas × pool stays within the server budget.
	MaxOpenConns    int           `json:"maxOpenConns"`
	MaxIdleConns    int           `json:"maxIdleConns"`
	ConnMaxLifetime time.Duration `json:"connMaxLifetime"`
	ConnMaxIdleTime time.Duration `json:"connMaxIdleTime"`
}

var validSSLModes = map[string]bool{
	"disable": true, "allow": true, "prefer": true,
	"require": true, "verify-ca": true, "verify-full": true,
}

// AMQPConfig holds AMQP/RabbitMQ configuration
type AMQPConfig struct {
	URL      string `json:"url"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// GeneralConfig holds general plugin configuration
type GeneralConfig struct {
	Environment string `json:"environment"`
	LogLevel    string `json:"logLevel"`
}

// ConfigError represents configuration-related errors
type ConfigError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e ConfigError) Error() string {
	return fmt.Sprintf("config error for %s: %s", e.Field, e.Message)
}

// CreateConfigService creates a new ConfigService by reading all environment variables
func CreateConfigService() (*ConfigService, error) {
	config := &ConfigService{}

	// Load database configuration
	dbConfig, err := loadDatabaseConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load database config: %w", err)
	}
	config.Database = dbConfig

	// Load AMQP configuration
	amqpConfig, err := loadAMQPConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load AMQP config: %w", err)
	}
	config.AMQP = amqpConfig

	// Load general configuration
	generalConfig := loadGeneralConfig()
	config.General = generalConfig

	return config, nil
}

// loadDatabaseConfig loads database configuration from environment variables
func loadDatabaseConfig() (DatabaseConfig, error) {
	var errors []ConfigError

	host := os.Getenv("PG_DB_HOST")
	if host == "" {
		errors = append(errors, ConfigError{"PG_DB_HOST", "required environment variable not set"})
	}

	port := os.Getenv("PG_DB_PORT")
	if port == "" {
		errors = append(errors, ConfigError{"PG_DB_PORT", "required environment variable not set"})
	}

	user := os.Getenv("PG_DB_USER")
	if user == "" {
		errors = append(errors, ConfigError{"PG_DB_USER", "required environment variable not set"})
	}

	password := os.Getenv("PG_DB_PASSWORD")
	if password == "" {
		errors = append(errors, ConfigError{"PG_DB_PASSWORD", "required environment variable not set"})
	} else if strings.HasPrefix(password, "!ChangeMe") {
		log.Printf("WARNING: PG_DB_PASSWORD is still set to a default placeholder — set a real password before deploying to production")
	}

	// Parse timeout with default
	timeout := 50 * time.Second
	if timeoutStr := os.Getenv("PG_DB_TIMEOUT_SECONDS"); timeoutStr != "" {
		if timeoutSeconds, err := strconv.Atoi(timeoutStr); err == nil {
			timeout = time.Duration(timeoutSeconds) * time.Second
		}
	}

	// Parse connection pool settings with defaults. These bound the per-instance
	// connection footprint; with a pooler (pgbouncer) in front of Postgres they can
	// be lowered further via env without code changes.
	maxOpenConns := envIntDefault("DB_MAX_OPEN_CONNS", 15)
	maxIdleConns := envIntDefault("DB_MAX_IDLE_CONNS", 3)
	connMaxLifetime := time.Duration(envIntDefault("DB_CONN_MAX_LIFETIME_SECONDS", 300)) * time.Second
	connMaxIdleTime := time.Duration(envIntDefault("DB_CONN_MAX_IDLE_SECONDS", 60)) * time.Second

	// SSL configuration
	sslMode := os.Getenv("PG_DB_SSLMODE")
	if sslMode == "" {
		env := os.Getenv("ENV")
		if env == "prod" || env == "production" {
			sslMode = "require"
		} else {
			sslMode = "disable"
		}
	}
	if !validSSLModes[sslMode] {
		errors = append(errors, ConfigError{"PG_DB_SSLMODE", fmt.Sprintf("invalid SSL mode: %s", sslMode)})
	}

	sslRootCert := os.Getenv("PG_DB_SSLROOTCERT")
	sslCert := os.Getenv("PG_DB_SSLCERT")
	sslKey := os.Getenv("PG_DB_SSLKEY")

	if len(errors) > 0 {
		return DatabaseConfig{}, fmt.Errorf("database configuration errors: %v", errors)
	}

	return DatabaseConfig{
		Host:            host,
		Port:            port,
		User:            user,
		Password:        password,
		SSLMode:         sslMode,
		SSLRootCert:     sslRootCert,
		SSLCert:         sslCert,
		SSLKey:          sslKey,
		Timeout:         timeout,
		MaxOpenConns:    maxOpenConns,
		MaxIdleConns:    maxIdleConns,
		ConnMaxLifetime: connMaxLifetime,
		ConnMaxIdleTime: connMaxIdleTime,
	}, nil
}

// envIntDefault reads an integer environment variable, returning def when the
// variable is unset or cannot be parsed.
func envIntDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed
		}
	}
	return def
}

// ApplyPool applies the configured connection pool bounds to a *sql.DB. This is
// the single choke point for pool sizing across all services and plugins.
func (c *DatabaseConfig) ApplyPool(sqldb *sql.DB) {
	sqldb.SetMaxOpenConns(c.MaxOpenConns)
	sqldb.SetMaxIdleConns(c.MaxIdleConns)
	sqldb.SetConnMaxLifetime(c.ConnMaxLifetime)
	sqldb.SetConnMaxIdleTime(c.ConnMaxIdleTime)
}

// loadAMQPConfig loads AMQP configuration from environment variables
func loadAMQPConfig() (AMQPConfig, error) {
	// Load individual AMQP fields with defaults
	protocol := os.Getenv("AMQP_PROTOCOL")
	if protocol == "" {
		protocol = "amqp"
	}

	host := os.Getenv("AMQP_HOST")
	if host == "" {
		host = "localhost"
	}

	port := os.Getenv("AMQP_PORT")
	if port == "" {
		port = "5672"
	}

	user := os.Getenv("AMQP_USER")
	if user == "" {
		user = "guest"
	}

	password := os.Getenv("AMQP_PASSWORD")
	if password == "" {
		password = "guest"
	}

	// Construct URL if not provided
	url := os.Getenv("AMQP_URL")
	if url == "" {
		url = fmt.Sprintf("%s://%s:%s@%s:%s/", protocol, user, password, host, port)
	}

	return AMQPConfig{
		URL:      url,
		Protocol: protocol,
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
	}, nil
}

// loadGeneralConfig loads general configuration from environment variables
func loadGeneralConfig() GeneralConfig {
	environment := os.Getenv("ENV")
	if environment == "" {
		environment = "dev" // default
	}

	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info" // default
	}

	return GeneralConfig{
		Environment: environment,
		LogLevel:    logLevel,
	}
}

// GetDatabaseDSN constructs a PostgreSQL DSN for the specified database
func (cs *ConfigService) GetDatabaseDSN(dbName string) string {
	var actualDBName string

	// Map logical database names to actual database names using dbhelper
	switch dbName {
	case "results", "codeclarity":
		actualDBName = dbhelper.Config.Database.Results
	case "knowledge":
		actualDBName = dbhelper.Config.Database.Knowledge
	case "plugins":
		actualDBName = dbhelper.Config.Database.Plugins
	case "config":
		actualDBName = dbhelper.Config.Database.Config
	default:
		actualDBName = dbName // Use as-is for custom databases
	}

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		url.QueryEscape(cs.Database.User),
		url.QueryEscape(cs.Database.Password),
		cs.Database.Host,
		cs.Database.Port,
		actualDBName,
		cs.Database.SSLMode,
	)
	if cs.Database.SSLRootCert != "" {
		dsn += "&sslrootcert=" + cs.Database.SSLRootCert
	}
	if cs.Database.SSLCert != "" {
		dsn += "&sslcert=" + cs.Database.SSLCert
	}
	if cs.Database.SSLKey != "" {
		dsn += "&sslkey=" + cs.Database.SSLKey
	}
	return dsn
}

// GetDatabaseTimeout returns the configured database timeout
func (cs *ConfigService) GetDatabaseTimeout() time.Duration {
	return cs.Database.Timeout
}

// IsProduction returns true if running in production environment
func (cs *ConfigService) IsProduction() bool {
	return cs.General.Environment == "prod" || cs.General.Environment == "production"
}

// IsDevelopment returns true if running in development environment
func (cs *ConfigService) IsDevelopment() bool {
	return cs.General.Environment == "dev" || cs.General.Environment == "development"
}

// GetLogLevel returns the configured log level
func (cs *ConfigService) GetLogLevel() string {
	return cs.General.LogLevel
}

// Validate validates the configuration and returns any errors
func (cs *ConfigService) Validate() error {
	var errors []string

	// Validate database configuration
	if cs.Database.Host == "" {
		errors = append(errors, "database host is required")
	}
	if cs.Database.Port == "" {
		errors = append(errors, "database port is required")
	}
	if cs.Database.User == "" {
		errors = append(errors, "database user is required")
	}
	if cs.Database.Password == "" {
		errors = append(errors, "database password is required")
	}
	if cs.Database.Timeout <= 0 {
		errors = append(errors, "database timeout must be positive")
	}

	if len(errors) > 0 {
		return fmt.Errorf("configuration validation failed: %v", errors)
	}

	return nil
}

// GetEnvironmentInfo returns a summary of the current environment configuration
func (cs *ConfigService) GetEnvironmentInfo() map[string]any {
	return map[string]any{
		"environment":      cs.General.Environment,
		"log_level":        cs.General.LogLevel,
		"database_host":    cs.Database.Host,
		"database_port":    cs.Database.Port,
		"database_user":    cs.Database.User,
		"database_timeout": cs.Database.Timeout.String(),
		"is_production":    cs.IsProduction(),
		"is_development":   cs.IsDevelopment(),
	}
}
