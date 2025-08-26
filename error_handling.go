package boilerplates

import (
	"fmt"
	"runtime"
	"time"
)

// ErrorSeverity defines the severity level of errors
type ErrorSeverity string

const (
	ErrorSeverityLow      ErrorSeverity = "low"
	ErrorSeverityMedium   ErrorSeverity = "medium"
	ErrorSeverityHigh     ErrorSeverity = "high"
	ErrorSeverityCritical ErrorSeverity = "critical"
)

// ErrorCategory defines the category of error
type ErrorCategory string

const (
	ErrorCategoryDatabase      ErrorCategory = "database"
	ErrorCategoryNetwork       ErrorCategory = "network"
	ErrorCategoryValidation    ErrorCategory = "validation"
	ErrorCategoryProcessing    ErrorCategory = "processing"
	ErrorCategoryConfiguration ErrorCategory = "configuration"
	ErrorCategoryExternal      ErrorCategory = "external"
	ErrorCategoryUnknown       ErrorCategory = "unknown"
)

// EcosystemError represents ecosystem-specific errors with rich context
type EcosystemError struct {
	// Core error information
	Message   string    `json:"message"`
	Cause     error     `json:"cause,omitempty"`
	Timestamp time.Time `json:"timestamp"`

	// Context information
	Ecosystem  string `json:"ecosystem,omitempty"`
	Plugin     string `json:"plugin,omitempty"`
	Stage      string `json:"stage,omitempty"`
	AnalysisID string `json:"analysisId,omitempty"`

	// Error classification
	Severity    ErrorSeverity `json:"severity"`
	Category    ErrorCategory `json:"category"`
	Recoverable bool          `json:"recoverable"`

	// Additional context
	StackTrace string                 `json:"stackTrace,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// Error implements the error interface
func (e *EcosystemError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s/%s] %s: %v", e.Ecosystem, e.Plugin, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s/%s] %s", e.Ecosystem, e.Plugin, e.Message)
}

// Unwrap allows error unwrapping
func (e *EcosystemError) Unwrap() error {
	return e.Cause
}

// IsCritical returns true if the error is critical
func (e *EcosystemError) IsCritical() bool {
	return e.Severity == ErrorSeverityCritical
}

// IsRecoverable returns true if the error is recoverable
func (e *EcosystemError) IsRecoverable() bool {
	return e.Recoverable
}

// NewEcosystemError creates a new ecosystem error
func NewEcosystemError(message string, cause error) *EcosystemError {
	return &EcosystemError{
		Message:     message,
		Cause:       cause,
		Timestamp:   time.Now(),
		Severity:    ErrorSeverityMedium,  // default
		Category:    ErrorCategoryUnknown, // default
		Recoverable: false,                // default
		Metadata:    make(map[string]interface{}),
	}
}

// EcosystemErrorBuilder provides a fluent interface for building ecosystem errors
type EcosystemErrorBuilder struct {
	error *EcosystemError
}

// NewErrorBuilder creates a new error builder
func NewErrorBuilder(message string) *EcosystemErrorBuilder {
	return &EcosystemErrorBuilder{
		error: &EcosystemError{
			Message:   message,
			Timestamp: time.Now(),
			Metadata:  make(map[string]interface{}),
		},
	}
}

// WithCause sets the underlying cause error
func (eb *EcosystemErrorBuilder) WithCause(cause error) *EcosystemErrorBuilder {
	eb.error.Cause = cause
	return eb
}

// WithEcosystem sets the ecosystem context
func (eb *EcosystemErrorBuilder) WithEcosystem(ecosystem string) *EcosystemErrorBuilder {
	eb.error.Ecosystem = ecosystem
	return eb
}

// WithPlugin sets the plugin context
func (eb *EcosystemErrorBuilder) WithPlugin(plugin string) *EcosystemErrorBuilder {
	eb.error.Plugin = plugin
	return eb
}

// WithStage sets the processing stage context
func (eb *EcosystemErrorBuilder) WithStage(stage string) *EcosystemErrorBuilder {
	eb.error.Stage = stage
	return eb
}

// WithAnalysisID sets the analysis ID context
func (eb *EcosystemErrorBuilder) WithAnalysisID(analysisID string) *EcosystemErrorBuilder {
	eb.error.AnalysisID = analysisID
	return eb
}

// WithSeverity sets the error severity
func (eb *EcosystemErrorBuilder) WithSeverity(severity ErrorSeverity) *EcosystemErrorBuilder {
	eb.error.Severity = severity
	return eb
}

// WithCategory sets the error category
func (eb *EcosystemErrorBuilder) WithCategory(category ErrorCategory) *EcosystemErrorBuilder {
	eb.error.Category = category
	return eb
}

// WithRecoverable sets whether the error is recoverable
func (eb *EcosystemErrorBuilder) WithRecoverable(recoverable bool) *EcosystemErrorBuilder {
	eb.error.Recoverable = recoverable
	return eb
}

// WithStackTrace captures the current stack trace
func (eb *EcosystemErrorBuilder) WithStackTrace() *EcosystemErrorBuilder {
	// Capture stack trace (simplified version)
	buf := make([]byte, 1024)
	n := runtime.Stack(buf, false)
	eb.error.StackTrace = string(buf[:n])
	return eb
}

// WithMetadata adds metadata to the error
func (eb *EcosystemErrorBuilder) WithMetadata(key string, value interface{}) *EcosystemErrorBuilder {
	eb.error.Metadata[key] = value
	return eb
}

// Build creates the final ecosystem error
func (eb *EcosystemErrorBuilder) Build() *EcosystemError {
	return eb.error
}

// Common error constructors for frequent scenarios

// NewDatabaseError creates a database-related error
func NewDatabaseError(message string, cause error, ecosystem, plugin string) *EcosystemError {
	return NewErrorBuilder(message).
		WithCause(cause).
		WithEcosystem(ecosystem).
		WithPlugin(plugin).
		WithCategory(ErrorCategoryDatabase).
		WithSeverity(ErrorSeverityHigh).
		WithRecoverable(true). // Database errors are often recoverable
		Build()
}

// NewValidationError creates a validation-related error
func NewValidationError(message string, ecosystem, plugin string) *EcosystemError {
	return NewErrorBuilder(message).
		WithEcosystem(ecosystem).
		WithPlugin(plugin).
		WithCategory(ErrorCategoryValidation).
		WithSeverity(ErrorSeverityMedium).
		WithRecoverable(false). // Validation errors are usually not recoverable
		Build()
}

// NewProcessingError creates a processing-related error
func NewProcessingError(message string, cause error, ecosystem, plugin, stage string) *EcosystemError {
	return NewErrorBuilder(message).
		WithCause(cause).
		WithEcosystem(ecosystem).
		WithPlugin(plugin).
		WithStage(stage).
		WithCategory(ErrorCategoryProcessing).
		WithSeverity(ErrorSeverityMedium).
		WithRecoverable(true).
		Build()
}

// NewConfigurationError creates a configuration-related error
func NewConfigurationError(message string, cause error) *EcosystemError {
	return NewErrorBuilder(message).
		WithCause(cause).
		WithCategory(ErrorCategoryConfiguration).
		WithSeverity(ErrorSeverityHigh).
		WithRecoverable(false). // Configuration errors usually need manual intervention
		Build()
}

// NewNetworkError creates a network-related error
func NewNetworkError(message string, cause error, ecosystem, plugin string) *EcosystemError {
	return NewErrorBuilder(message).
		WithCause(cause).
		WithEcosystem(ecosystem).
		WithPlugin(plugin).
		WithCategory(ErrorCategoryNetwork).
		WithSeverity(ErrorSeverityMedium).
		WithRecoverable(true). // Network errors are often transient
		Build()
}

// ErrorHandler provides centralized error handling functionality
type ErrorHandler struct {
	pluginName string
	ecosystem  string
}

// NewErrorHandler creates a new error handler for a specific plugin/ecosystem
func NewErrorHandler(pluginName, ecosystem string) *ErrorHandler {
	return &ErrorHandler{
		pluginName: pluginName,
		ecosystem:  ecosystem,
	}
}

// HandleError processes an error and returns an appropriate EcosystemError
func (eh *ErrorHandler) HandleError(err error, stage string) *EcosystemError {
	if err == nil {
		return nil
	}

	// If it's already an EcosystemError, enhance it with context
	if ecosErr, ok := err.(*EcosystemError); ok {
		if ecosErr.Ecosystem == "" {
			ecosErr.Ecosystem = eh.ecosystem
		}
		if ecosErr.Plugin == "" {
			ecosErr.Plugin = eh.pluginName
		}
		if ecosErr.Stage == "" {
			ecosErr.Stage = stage
		}
		return ecosErr
	}

	// Create a new EcosystemError from the generic error
	return NewProcessingError(err.Error(), err, eh.ecosystem, eh.pluginName, stage)
}

// WrapWithContext wraps an error with ecosystem context
func (eh *ErrorHandler) WrapWithContext(err error, message, stage string) *EcosystemError {
	return NewProcessingError(message, err, eh.ecosystem, eh.pluginName, stage)
}

// ErrorRecoveryStrategy defines how to handle different types of errors
type ErrorRecoveryStrategy struct {
	MaxRetries      int           `json:"maxRetries"`
	RetryDelay      time.Duration `json:"retryDelay"`
	BackoffFactor   float64       `json:"backoffFactor"`
	RecoverableOnly bool          `json:"recoverableOnly"`
}

// DefaultRecoveryStrategy returns a sensible default recovery strategy
func DefaultRecoveryStrategy() ErrorRecoveryStrategy {
	return ErrorRecoveryStrategy{
		MaxRetries:      3,
		RetryDelay:      1 * time.Second,
		BackoffFactor:   2.0,
		RecoverableOnly: true,
	}
}

// ShouldRetry determines if an error should be retried based on the strategy
func (strategy ErrorRecoveryStrategy) ShouldRetry(err *EcosystemError, attemptCount int) bool {
	if attemptCount >= strategy.MaxRetries {
		return false
	}

	if strategy.RecoverableOnly && !err.IsRecoverable() {
		return false
	}

	// Don't retry critical configuration errors
	if err.Category == ErrorCategoryConfiguration && err.Severity == ErrorSeverityCritical {
		return false
	}

	return true
}

// GetRetryDelay calculates the delay before the next retry attempt
func (strategy ErrorRecoveryStrategy) GetRetryDelay(attemptCount int) time.Duration {
	if attemptCount <= 0 {
		return strategy.RetryDelay
	}

	// Exponential backoff
	multiplier := 1.0
	for i := 0; i < attemptCount; i++ {
		multiplier *= strategy.BackoffFactor
	}

	return time.Duration(float64(strategy.RetryDelay) * multiplier)
}
