package boilerplates

import (
	"fmt"
	"log"
)

// LicenseWorkspaceInfo represents workspace license information in a generic way
// This mirrors the structure from the license plugin without importing it directly
type LicenseWorkspaceInfo struct {
	LicensesDepMap              map[string][]string              `json:"licensesDepMap"`
	NonSpdxLicensesDepMap       map[string][]string              `json:"nonSpdxLicensesDepMap"`
	LicenseComplianceViolations []string                         `json:"licenseComplianceViolations"`
	DependencyInfo              map[string]LicenseDependencyInfo `json:"dependencyInfo"`
}

// LicenseDependencyInfo represents dependency information for licenses
type LicenseDependencyInfo struct {
	Name              string   `json:"name"`
	Version           string   `json:"version"`
	Licenses          []string `json:"licenses"`
	LicenseExpression string   `json:"licenseExpression"`
	LicenseViolations []string `json:"licenseViolations"`
}

// LicenseAnalysisStats represents license analysis statistics
type LicenseAnalysisStats struct {
	NumberOfSpdxLicenses       int            `json:"numberOfSpdxLicenses"`
	NumberOfNonSpdxLicenses    int            `json:"numberOfNonSpdxLicenses"`
	NumberOfCopyLeftLicenses   int            `json:"numberOfCopyLeftLicenses"`
	NumberOfPermissiveLicenses int            `json:"numberOfPermissiveLicenses"`
	LicenseDist                map[string]int `json:"licenseDist"`
}

// LicenseOutput represents the complete license analysis output
type LicenseOutput struct {
	WorkSpaces     map[string]LicenseWorkspaceInfo `json:"workSpaces"`
	AnalysisStats  LicenseAnalysisStats            `json:"analysisStats"`
	AnalysisStatus string                          `json:"status"`
}

// LicenseResultMerger provides merge functionality for license analysis results
type LicenseResultMerger struct {
	*GenericResultMerger[LicenseOutput]
	utils *MergerUtils
}

// NewLicenseResultMerger creates a new license result merger
func NewLicenseResultMerger(strategy MergeStrategy) *LicenseResultMerger {
	utils := NewMergerUtils()

	merger := &LicenseResultMerger{
		GenericResultMerger: NewGenericResultMerger(strategy, mergeLicenseOutputs),
		utils:               utils,
	}

	return merger
}

// mergeLicenseOutputs is the merge function for license outputs
func mergeLicenseOutputs(outputs []LicenseOutput) LicenseOutput {
	if len(outputs) == 0 {
		return LicenseOutput{
			WorkSpaces:     make(map[string]LicenseWorkspaceInfo),
			AnalysisStats:  LicenseAnalysisStats{},
			AnalysisStatus: "FAILURE",
		}
	}

	if len(outputs) == 1 {
		return outputs[0]
	}

	utils := NewMergerUtils()

	// Start with the first output as base
	merged := LicenseOutput{
		WorkSpaces:     make(map[string]LicenseWorkspaceInfo),
		AnalysisStats:  LicenseAnalysisStats{LicenseDist: make(map[string]int)},
		AnalysisStatus: "SUCCESS",
	}

	// Track if any analysis failed
	hasFailures := false

	// Merge all outputs
	for _, output := range outputs {
		if output.AnalysisStatus != "SUCCESS" {
			hasFailures = true
			log.Printf("Warning: merging failed license analysis output")
			continue
		}

		// Merge workspaces
		for workspaceName, workspace := range output.WorkSpaces {
			if existingWorkspace, exists := merged.WorkSpaces[workspaceName]; exists {
				// Merge existing workspace
				merged.WorkSpaces[workspaceName] = mergeLicenseWorkspaces(existingWorkspace, workspace, utils)
			} else {
				// Add new workspace
				merged.WorkSpaces[workspaceName] = workspace
			}
		}

		// Merge analysis stats
		merged.AnalysisStats.NumberOfSpdxLicenses += output.AnalysisStats.NumberOfSpdxLicenses
		merged.AnalysisStats.NumberOfNonSpdxLicenses += output.AnalysisStats.NumberOfNonSpdxLicenses
		merged.AnalysisStats.NumberOfCopyLeftLicenses += output.AnalysisStats.NumberOfCopyLeftLicenses
		merged.AnalysisStats.NumberOfPermissiveLicenses += output.AnalysisStats.NumberOfPermissiveLicenses

		// Merge license distribution
		if output.AnalysisStats.LicenseDist != nil {
			if merged.AnalysisStats.LicenseDist == nil {
				merged.AnalysisStats.LicenseDist = make(map[string]int)
			}
			merged.AnalysisStats.LicenseDist = utils.MergeIntMaps(merged.AnalysisStats.LicenseDist, output.AnalysisStats.LicenseDist)
		}
	}

	// Set final status
	if hasFailures && len(merged.WorkSpaces) == 0 {
		merged.AnalysisStatus = "FAILURE"
	}

	utils.LogMergeOperation(
		"license analysis",
		len(outputs),
		fmt.Sprintf("%d workspaces", len(merged.WorkSpaces)),
	)

	return merged
}

// mergeLicenseWorkspaces merges two license workspace infos
func mergeLicenseWorkspaces(existing, new LicenseWorkspaceInfo, utils *MergerUtils) LicenseWorkspaceInfo {
	merged := LicenseWorkspaceInfo{
		LicensesDepMap:              utils.MergeStringMaps(existing.LicensesDepMap, new.LicensesDepMap),
		NonSpdxLicensesDepMap:       utils.MergeStringMaps(existing.NonSpdxLicensesDepMap, new.NonSpdxLicensesDepMap),
		LicenseComplianceViolations: utils.MergeStringSlices(existing.LicenseComplianceViolations, new.LicenseComplianceViolations),
		DependencyInfo:              mergeDependencyInfo(existing.DependencyInfo, new.DependencyInfo, utils),
	}

	return merged
}

// mergeDependencyInfo merges dependency information maps
func mergeDependencyInfo(existing, new map[string]LicenseDependencyInfo, utils *MergerUtils) map[string]LicenseDependencyInfo {
	merged := make(map[string]LicenseDependencyInfo)

	// Copy existing dependencies
	for key, info := range existing {
		merged[key] = info
	}

	// Add or merge new dependencies
	for key, newInfo := range new {
		if existingInfo, exists := merged[key]; exists {
			// Merge dependency info
			merged[key] = LicenseDependencyInfo{
				Name:              newInfo.Name,    // Use new name (should be the same)
				Version:           newInfo.Version, // Use new version (should be the same)
				Licenses:          utils.MergeStringSlices(existingInfo.Licenses, newInfo.Licenses),
				LicenseExpression: mergeLicenseExpression(existingInfo.LicenseExpression, newInfo.LicenseExpression),
				LicenseViolations: utils.MergeStringSlices(existingInfo.LicenseViolations, newInfo.LicenseViolations),
			}
		} else {
			merged[key] = newInfo
		}
	}

	return merged
}

// mergeLicenseExpression combines license expressions (simple implementation)
func mergeLicenseExpression(existing, new string) string {
	if existing == "" {
		return new
	}
	if new == "" {
		return existing
	}
	if existing == new {
		return existing
	}
	// If they're different, combine with OR
	return fmt.Sprintf("(%s) OR (%s)", existing, new)
}
