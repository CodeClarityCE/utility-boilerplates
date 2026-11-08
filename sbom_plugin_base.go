package boilerplates

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	types_amqp "github.com/CodeClarityCE/utility-types/amqp"
	codeclarity "github.com/CodeClarityCE/utility-types/codeclarity_db"
	"github.com/CodeClarityCE/utility-types/exceptions"
	plugin_db "github.com/CodeClarityCE/utility-types/plugin_db"
)

// SBOMAnalyzer defines the interface that all language-specific SBOM analyzers must implement
type SBOMAnalyzer interface {
	// AnalyzeProject performs the SBOM analysis for a specific language
	AnalyzeProject(projectPath string, analysisId string, knowledgeDB any) (SBOMOutput, error)

	// CanAnalyze checks if this analyzer can handle the given project
	CanAnalyze(projectPath string) bool

	// GetLanguage returns the language this analyzer handles
	GetLanguage() string

	// DetectFramework detects the framework used in the project (optional, can return empty string)
	DetectFramework(projectPath string) string

	// ConvertToMap converts the analyzer's output to map[string]any for storage
	ConvertToMap(output SBOMOutput) map[string]any

	// GetDependencyCount returns the total number of dependencies found
	GetDependencyCount(output SBOMOutput) int
}

// SBOMOutput represents the standard output structure for all SBOM analyzers
type SBOMOutput interface {
	GetStatus() codeclarity.AnalysisStatus
	GetFramework() string
}

// GenericSBOMHandler implements the AnalysisHandler interface for SBOM plugins
type GenericSBOMHandler struct {
	Analyzer SBOMAnalyzer
}

// StartAnalysis implements the AnalysisHandler interface
func (h *GenericSBOMHandler) StartAnalysis(
	databases *PluginDatabases,
	dispatcherMessage types_amqp.DispatcherPluginMessage,
	config plugin_db.Plugin,
	analysisDoc codeclarity.Analysis,
) (map[string]any, codeclarity.AnalysisStatus, error) {
	return h.performSBOMAnalysis(databases, dispatcherMessage, config, analysisDoc)
}

// performSBOMAnalysis performs the standardized SBOM analysis workflow
func (h *GenericSBOMHandler) performSBOMAnalysis(
	databases *PluginDatabases,
	dispatcherMessage types_amqp.DispatcherPluginMessage,
	config plugin_db.Plugin,
	analysisDoc codeclarity.Analysis,
) (map[string]any, codeclarity.AnalysisStatus, error) {
	// Extract project path from analysis configuration
	projectPath, err := h.extractProjectPath(analysisDoc, config)
	if err != nil {
		return h.handleFailure(databases, dispatcherMessage, config, err, "the project source could not be found")
	}

	// Check if the analyzer can handle this project
	if !h.Analyzer.CanAnalyze(projectPath) {
		err := fmt.Errorf("project at %s cannot be analyzed by %s analyzer", projectPath, h.Analyzer.GetLanguage())
		return h.handleFailure(databases, dispatcherMessage, config, err, fmt.Sprintf("no %s manifest was found in the project", h.Analyzer.GetLanguage()))
	}

	// Perform the analysis
	log.Printf("%s SBOM Analysis - Starting analysis for project: %s", h.Analyzer.GetLanguage(), projectPath)

	var knowledgeDB any
	if databases.Knowledge != nil {
		knowledgeDB = databases.Knowledge
	}

	sbomOutput, err := h.Analyzer.AnalyzeProject(projectPath, analysisDoc.Id.String(), knowledgeDB)
	if err != nil {
		return h.handleFailure(databases, dispatcherMessage, config, err, h.analysisFailedReason())
	}

	// Store the result
	outputMap := h.Analyzer.ConvertToMap(sbomOutput)
	result := codeclarity.Result{
		Result:     outputMap,
		AnalysisId: dispatcherMessage.AnalysisId,
		Plugin:     config.Name,
		CreatedOn:  time.Now(),
	}

	_, err = databases.Codeclarity.NewInsert().Model(&result).Exec(context.Background())
	if err != nil {
		return nil, codeclarity.FAILURE, fmt.Errorf("failed to save result: %w", err)
	}

	// Prepare the response
	response := map[string]any{
		"sbomKey":      result.Id,
		"packageCount": h.Analyzer.GetDependencyCount(sbomOutput),
		"framework":    sbomOutput.GetFramework(),
		"language":     h.Analyzer.GetLanguage(),
	}

	if sbomOutput.GetStatus() == codeclarity.FAILURE {
		reason := latestPublicError(outputMap)
		if reason == "" {
			reason = h.analysisFailedReason()
		}
		response[codeclarity.StepFailureReasonKey] = reason
		log.Printf("%s SBOM Analysis - Failed: %s", h.Analyzer.GetLanguage(), reason)
		return response, codeclarity.FAILURE, nil
	}

	log.Printf("%s SBOM Analysis - Completed successfully. Dependencies: %d, Framework: %s",
		h.Analyzer.GetLanguage(), h.Analyzer.GetDependencyCount(sbomOutput), sbomOutput.GetFramework())

	return response, sbomOutput.GetStatus(), nil
}

// analysisFailedReason is the reason shown when the analyzer gave no readable cause.
func (h *GenericSBOMHandler) analysisFailedReason() string {
	return fmt.Sprintf("the %s dependencies could not be analyzed", h.Analyzer.GetLanguage())
}

// latestPublicError returns the newest public error in an SBOM output, or "".
func latestPublicError(output map[string]any) string {
	raw, err := json.Marshal(output)
	if err != nil {
		return ""
	}
	var parsed struct {
		AnalysisInfo struct {
			Errors []exceptions.Error `json:"errors"`
		} `json:"analysis_info"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	errs := parsed.AnalysisInfo.Errors
	if len(errs) == 0 {
		return ""
	}
	return errs[len(errs)-1].Public.Description
}

// extractProjectPath extracts and validates the project path from the analysis configuration
func (h *GenericSBOMHandler) extractProjectPath(analysisDoc codeclarity.Analysis, config plugin_db.Plugin) (string, error) {
	// Get analysis config
	messageData, ok := analysisDoc.Config[config.Name].(map[string]any)
	if !ok {
		return "", fmt.Errorf("analysis configuration not found for plugin %s", config.Name)
	}

	// Get download path from environment
	basePath := os.Getenv("DOWNLOAD_PATH")
	if basePath == "" {
		basePath = "/private" // Default path
	}

	// Get project path from config
	projectInterface, ok := messageData["project"]
	if !ok || projectInterface == nil {
		return "", fmt.Errorf("project path not provided in analysis configuration")
	}

	projectPath := basePath + "/" + projectInterface.(string)

	// Validate project path exists
	if _, err := os.Stat(projectPath); os.IsNotExist(err) {
		return "", fmt.Errorf("project path does not exist: %s", projectPath)
	}

	return projectPath, nil
}

// handleFailure creates a failure result and stores it in the database
func (h *GenericSBOMHandler) handleFailure(
	databases *PluginDatabases,
	dispatcherMessage types_amqp.DispatcherPluginMessage,
	config plugin_db.Plugin,
	originalError error,
	reason string,
) (map[string]any, codeclarity.AnalysisStatus, error) {
	log.Printf("%s SBOM Analysis - Failure: %s - %v", h.Analyzer.GetLanguage(), reason, originalError)

	// Create failure output with standardized error structure
	failureOutput := map[string]any{
		"analysisInfo": map[string]any{
			"status": codeclarity.FAILURE,
			"errors": []exceptions.Error{
				{
					Public: exceptions.ErrorContent{
						Type:        exceptions.GENERIC_ERROR,
						Description: reason,
					},
					Private: exceptions.ErrorContent{
						Type:        "SBOMAnalysisException",
						Description: originalError.Error(),
					},
				},
			},
		},
		"workspaces": []any{},
	}

	// Store the failure result
	result := codeclarity.Result{
		Result:     failureOutput,
		AnalysisId: dispatcherMessage.AnalysisId,
		Plugin:     config.Name,
		CreatedOn:  time.Now(),
	}

	_, err := databases.Codeclarity.NewInsert().Model(&result).Exec(context.Background())
	if err != nil {
		log.Printf("Failed to save failure result: %v", err)
		// Don't return this error, return the original analysis error
	}

	return map[string]any{"sbomKey": result.Id, codeclarity.StepFailureReasonKey: reason}, codeclarity.FAILURE, nil
}

// CreateSBOMPlugin creates and starts a complete SBOM plugin with the given analyzer
func CreateSBOMPlugin(analyzer SBOMAnalyzer) error {
	pluginBase, err := CreatePluginBase()
	if err != nil {
		return fmt.Errorf("failed to initialize plugin base: %w", err)
	}
	defer pluginBase.Close()

	handler := &GenericSBOMHandler{Analyzer: analyzer}

	log.Printf("Starting %s SBOM plugin", analyzer.GetLanguage())
	err = pluginBase.Listen(handler)
	if err != nil {
		return fmt.Errorf("failed to start plugin: %w", err)
	}

	return nil
}
