package boilerplates

// MergeStrategy defines different strategies for merging multi-language results
// This is a temporary definition until the ecosystem package is published remotely
type MergeStrategy string

const (
	MergeStrategyUnion        MergeStrategy = "union"        // Combine all results
	MergeStrategyIntersection MergeStrategy = "intersection" // Only results present in all languages
	MergeStrategyPriority     MergeStrategy = "priority"     // Prioritize results from specific languages
)
