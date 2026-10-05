package seqvalidation

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

const (
	errorPrefix         = "seqvalidation: "
	errorSeparator      = ": "
	groupLabelFormat    = "group %d"
	ruleLabelFormat     = " rule %d"
	reasonEmptySequence = "empty sequence"
	reasonEmptyGroup    = "empty group"
	reasonNilRule       = "nil rule"
	reasonNilContext    = "nil context"
	reasonNilState      = "nil state"
)

// Rule carries the field type F so groups and sequences require a common field type.
// It returns application-defined violations and may enrich state. Its error
// takes precedence over violations and cancellation observed after it returns.
// Every returned element counts as a violation, including nil pointers. Use Field
// to skip nil entries from pointer-result checks.
type Rule[F, T, V any] func(ctx context.Context, state *T) ([]V, error)

// Field creates a field check. Nil result entries mean success and are skipped.
// It preserves result pointers and order without modifying the check's slice.
// A nil check produces a nil rule so configuration preflight rejects it.
func Field[F, T, V any](field F, check func(context.Context, F, *T) ([]*V, error)) Rule[F, T, *V] {
	if check == nil {
		return nil
	}

	return func(ctx context.Context, state *T) ([]*V, error) {
		found, err := check(ctx, field, state)
		if err != nil {
			return nil, err
		}

		var violations []*V
		for _, violation := range found {
			if violation != nil {
				violations = append(violations, violation)
			}
		}

		return violations, nil
	}
}

// Stage describes a group of rules. Create stages with Group.
type Stage[F, T, V any] struct {
	name  string
	rules []Rule[F, T, V]
}

// Group creates a stage with a snapshot of rules in declaration order.
// Validate collects violations within the stage before deciding whether to
// continue, unless a rule errors or the context is canceled. Empty groups are invalid.
func Group[F, T, V any](rules ...Rule[F, T, V]) Stage[F, T, V] {
	return Stage[F, T, V]{rules: slices.Clone(rules)}
}

// Named returns a copy with a diagnostic name. It does not change execution
// order or select rules; the name appears in configuration and execution errors.
func (s Stage[F, T, V]) Named(name string) Stage[F, T, V] {
	s.name = name
	return s
}

// Rules describes an ordered sequence of stages. Its zero value is invalid.
// Concurrent calls are safe with distinct states and rules that do not share
// unsafe mutable dependencies.
type Rules[F, T, V any] struct {
	stages []Stage[F, T, V]
}

// Sequence snapshots groups in execution order. It does not run or validate them;
// use Check at startup or rely on Validate's configuration preflight.
func Sequence[F, T, V any](groups ...Stage[F, T, V]) Rules[F, T, V] {
	stages := make([]Stage[F, T, V], len(groups))
	for i, group := range groups {
		stages[i] = Group(group.rules...).Named(group.name)
	}

	return Rules[F, T, V]{stages: stages}
}

// ConfigError identifies invalid configuration before any rule runs.
// Indexes are zero-based; -1 means no particular group or rule applies.
type ConfigError struct {
	GroupIndex int
	GroupName  string
	RuleIndex  int
	Reason     string
}

func (e *ConfigError) Error() string {
	if e.GroupIndex < 0 {
		return errorPrefix + e.Reason
	}

	return errorPrefix + errorLocation(e.GroupIndex, e.GroupName, e.RuleIndex) + errorSeparator + e.Reason
}

// ExecutionError identifies the rule where execution failed or cancellation
// was observed. It wraps the original cause. Indexes are zero-based.
type ExecutionError struct {
	GroupIndex int
	GroupName  string
	RuleIndex  int
	Err        error
}

func (e *ExecutionError) Error() string {
	return errorPrefix + errorLocation(e.GroupIndex, e.GroupName, e.RuleIndex) + errorSeparator + fmt.Sprint(e.Err)
}

func (e *ExecutionError) Unwrap() error { return e.Err }

func errorLocation(groupIndex int, groupName string, ruleIndex int) string {
	location := fmt.Sprintf(groupLabelFormat, groupIndex)
	if groupName != "" {
		location += fmt.Sprintf(" (%q)", groupName)
	}

	if ruleIndex >= 0 {
		location += fmt.Sprintf(ruleLabelFormat, ruleIndex)
	}

	return location
}

var (
	// ErrNilContext is returned when Validate receives a nil context.
	ErrNilContext = errors.New(errorPrefix + reasonNilContext)

	// ErrNilState is returned when Validate receives a nil state.
	ErrNilState = errors.New(errorPrefix + reasonNilState)
)

// Check rejects the first empty sequence, empty group, or nil rule without
// executing any rule. This includes invalid groups after an otherwise failing group.
func (p Rules[F, T, V]) Check() error {
	if len(p.stages) == 0 {
		return &ConfigError{GroupIndex: -1, RuleIndex: -1, Reason: reasonEmptySequence}
	}

	for i, stage := range p.stages {
		if len(stage.rules) == 0 {
			return &ConfigError{GroupIndex: i, GroupName: stage.name, RuleIndex: -1, Reason: reasonEmptyGroup}
		}

		for j, rule := range stage.rules {
			if rule == nil {
				return &ConfigError{GroupIndex: i, GroupName: stage.name, RuleIndex: j, Reason: reasonNilRule}
			}
		}
	}

	return nil
}

// Validate checks all configuration before running groups in declaration order.
// It returns all violations of the first failing group, or nil, nil on success.
// A rule error immediately returns nil, error, ahead of any cancellation detected
// after that rule. Cancellation is checked before and after each rule but cannot
// interrupt one that ignores ctx. State changes are never rolled back.
func (p Rules[F, T, V]) Validate(ctx context.Context, state *T) ([]V, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}

	if state == nil {
		return nil, ErrNilState
	}

	if err := p.Check(); err != nil {
		return nil, err
	}

	for i, stage := range p.stages {
		var violations []V

		for j, rule := range stage.rules {
			wrap := func(err error) error {
				return &ExecutionError{
					GroupIndex: i,
					GroupName:  stage.name,
					RuleIndex:  j,
					Err:        err,
				}
			}

			if err := ctx.Err(); err != nil {
				return nil, wrap(err)
			}

			found, err := rule(ctx, state)
			if err != nil {
				return nil, wrap(err)
			}

			if err := ctx.Err(); err != nil {
				return nil, wrap(err)
			}

			violations = append(violations, found...)
		}

		if len(violations) > 0 {
			return violations, nil
		}
	}

	return nil, nil
}
