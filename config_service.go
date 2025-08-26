package boilerplates

import (
	"fmt"
	"os"
	"strconv"
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
	Host     string        `json:"host"`
	Port     string        `json:"port"`
	User     string        `json:"user"`
	Password string        `json:"password"`
	Timeout  time.Duration `json:"timeout"`
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
	}

	// Parse timeout with default
	timeout := 50 * time.Second
	if timeoutStr := os.Getenv("PG_DB_TIMEOUT_SECONDS"); timeoutStr != "" {
		if timeoutSeconds, err := strconv.Atoi(timeoutStr); err == nil {
			timeout = time.Duration(timeoutSeconds) * time.Second
		}
	}

	if len(errors) > 0 {
		return DatabaseConfig{}, fmt.Errorf("database configuration errors: %v", errors)
	}

	return DatabaseConfig{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		Timeout:  timeout,
	}, nil
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
	case "plugins", "config":
		actualDBName = dbhelper.Config.Database.Plugins
	default:
		actualDBName = dbName // Use as-is for custom databases
	}

	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		cs.Database.User,
		cs.Database.Password,
		cs.Database.Host,
		cs.Database.Port,
		actualDBName,
	)
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
func (cs *ConfigService) GetEnvironmentInfo() map[string]interface{} {
	return map[string]interface{}{
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
