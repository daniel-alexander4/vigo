// Package runner provides the convergence runner and supporting utilities.
package runner

// EvaluateWhen evaluates a Go template expression against a facts map.
// An empty expression returns true (no condition means always run).
func EvaluateWhen(expr string, facts map[string]any) (bool, error) {
	// Stub -- will be implemented in GREEN phase
	return false, nil
}
