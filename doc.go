// Package seqvalidation executes ordered groups of validation rules against
// application-owned state. It returns the violations of the first failing group
// or an execution error that preserves the original cause. Field adapts checks
// with a field argument and skips nil pointer results.
package seqvalidation
