package boilerplates

import (
	"fmt"
	"log"
)

// GenericResultMerger provides generic functionality for merging analysis results
type GenericResultMerger[T any] struct {
	strategy      MergeStrategy
	mergeFunction func([]T) T
}

// NewGenericResultMerger creates a new generic result merger with a custom merge function
func NewGenericResultMerger[T any](strategy MergeStrategy, mergeFunc func([]T) T) *GenericResultMerger[T] {
	return &GenericResultMerger[T]{
		strategy:      strategy,
		mergeFunction: mergeFunc,
	}
}

// MergeWorkspaces merges results from multiple language ecosystems
func (m *GenericResultMerger[T]) MergeWorkspaces(results []T) T {
	if len(results) == 0 {
		var zero T
		return zero
	}

	if len(results) == 1 {
		return results[0]
	}

	return m.mergeFunction(results)
}

// GetMergeStrategy returns the merge strategy used by this merger
func (m *GenericResultMerger[T]) GetMergeStrategy() string {
	return string(m.strategy)
}

// WorkspaceKey represents a unique identifier for a workspace across multiple ecosystems
type WorkspaceKey struct {
	Name      string
	Ecosystem string
}

// String returns the string representation of the workspace key
func (wk WorkspaceKey) String() string {
	if wk.Ecosystem == "" {
		return wk.Name
	}
	return fmt.Sprintf("%s_%s", wk.Name, wk.Ecosystem)
}

// MergerUtils provides utility functions for common merge operations
type MergerUtils struct{}

// NewMergerUtils creates a new instance of merger utilities
func NewMergerUtils() *MergerUtils {
	return &MergerUtils{}
}

// MergeStringSlices merges multiple string slices, removing duplicates
func (u *MergerUtils) MergeStringSlices(slices ...[]string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, slice := range slices {
		for _, item := range slice {
			if !seen[item] {
				seen[item] = true
				result = append(result, item)
			}
		}
	}

	return result
}

// MergeStringMaps merges multiple string maps, combining values for duplicate keys
func (u *MergerUtils) MergeStringMaps(maps ...map[string][]string) map[string][]string {
	result := make(map[string][]string)

	for _, m := range maps {
		for key, values := range m {
			if existing, exists := result[key]; exists {
				result[key] = u.MergeStringSlices(existing, values)
			} else {
				result[key] = values
			}
		}
	}

	return result
}

// MergeIntMaps merges multiple int maps, summing values for duplicate keys
func (u *MergerUtils) MergeIntMaps(maps ...map[string]int) map[string]int {
	result := make(map[string]int)

	for _, m := range maps {
		for key, value := range m {
			result[key] += value
		}
	}

	return result
}

// LogMergeOperation logs information about a merge operation
func (u *MergerUtils) LogMergeOperation(operation string, inputCount int, outputDescription string) {
	log.Printf("Merge operation: %s - processed %d inputs, result: %s", operation, inputCount, outputDescription)
}

// ValidateNonEmptySlice checks if a slice is not empty and logs a warning if it is
func (u *MergerUtils) ValidateNonEmptySlice(slice []interface{}, description string) bool {
	if len(slice) == 0 {
		log.Printf("Warning: %s is empty during merge operation", description)
		return false
	}
	return true
}
