package seqvalidation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/egor-ponomarev/seqvalidation"
)

type appViolation struct {
	field  string
	params map[string]any
}

func TestFieldPreservesApplicationResults(t *testing.T) {
	// Arrange
	type field string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &state{}
	first := &appViolation{field: "subject", params: map[string]any{"limit": 100}}
	second := &appViolation{field: "body"}
	backing := []*appViolation{nil, first, nil, second, nil}
	var gotContext context.Context
	var gotState *state
	var gotField field
	check := func(ctx context.Context, name field, value *state) ([]*appViolation, error) {
		gotContext, gotField, gotState = ctx, name, value
		value.trace = append(value.trace, "check")
		return backing, nil
	}
	next := func(_ context.Context, value *state) ([]*appViolation, error) {
		value.trace = append(value.trace, "same_group")
		return nil, nil
	}
	later := func(_ context.Context, value *state) ([]*appViolation, error) {
		value.trace = append(value.trace, "later_group")
		return nil, nil
	}
	rules := seqvalidation.Sequence(
		seqvalidation.Group(seqvalidation.Field(field("subject"), check), next),
		seqvalidation.Group[field, state, *appViolation](later),
	)

	// Act
	found, err := rules.Validate(ctx, s)

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	if gotContext != ctx || gotState != s || gotField != field("subject") {
		t.Fatalf("bound arguments: context=%v state=%p field=%q", gotContext, gotState, gotField)
	}
	if len(found) != 2 || found[0] != first || found[1] != second {
		t.Fatalf("results must preserve pointer identity and order: %v", found)
	}
	if !reflect.DeepEqual(s.trace, []string{"check", "same_group"}) {
		t.Fatalf("calls: %v", s.trace)
	}
	if !reflect.DeepEqual(backing, []*appViolation{nil, first, nil, second, nil}) {
		t.Fatalf("input slice was changed: %v", backing)
	}
}

func TestFieldEmptyResultsAllowNextGroup(t *testing.T) {
	for _, result := range [][]*appViolation{nil, {}, {nil, nil}} {
		t.Run("empty", func(t *testing.T) {
			// Arrange
			s := &state{}
			check := func(context.Context, string, *state) ([]*appViolation, error) {
				return result, nil
			}
			next := func(_ context.Context, value *state) ([]*appViolation, error) {
				value.trace = append(value.trace, "next")
				return nil, nil
			}
			rules := seqvalidation.Sequence(
				seqvalidation.Group(seqvalidation.Field("subject", check)),
				seqvalidation.Group[string, state, *appViolation](next),
			)

			// Act
			found, err := rules.Validate(context.Background(), s)

			// Assert
			if err != nil || found != nil || !reflect.DeepEqual(s.trace, []string{"next"}) {
				t.Fatalf("found=%v err=%v calls=%v", found, err, s.trace)
			}
		})
	}
}

func TestFieldRuleErrorWinsOverCancellation(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cause := errors.New("lookup failed")
	s := &state{}
	check := func(_ context.Context, _ string, value *state) ([]*appViolation, error) {
		value.trace = append(value.trace, "check")
		cancel()
		return []*appViolation{{field: "subject"}}, cause
	}
	next := func(_ context.Context, value *state) ([]*appViolation, error) {
		value.trace = append(value.trace, "unreachable")
		return nil, nil
	}
	rules := seqvalidation.Sequence(seqvalidation.Group(seqvalidation.Field("subject", check), next))

	// Act
	found, err := rules.Validate(ctx, s)

	// Assert
	if found != nil || !errors.Is(err, cause) || !reflect.DeepEqual(s.trace, []string{"check"}) {
		t.Fatalf("found=%v err=%v calls=%v", found, err, s.trace)
	}
}

func TestFieldNilCheckFailsPreflight(t *testing.T) {
	// Arrange
	s := &state{}
	var check func(context.Context, string, *state) ([]*appViolation, error)
	first := func(_ context.Context, value *state) ([]*appViolation, error) {
		value.trace = append(value.trace, "unreachable")
		return nil, nil
	}
	rules := seqvalidation.Sequence(
		seqvalidation.Group[string, state, *appViolation](first),
		seqvalidation.Group(seqvalidation.Field("subject", check)).Named("bound"),
	)

	// Act
	found, err := rules.Validate(context.Background(), s)

	// Assert
	var config *seqvalidation.ConfigError
	if found != nil || !errors.As(err, &config) {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if config.GroupIndex != 1 || config.GroupName != "bound" || config.RuleIndex != 0 || config.Reason != "nil rule" {
		t.Fatalf("configuration error: %+v", config)
	}
	if len(s.trace) != 0 {
		t.Fatalf("rules ran before preflight: %v", s.trace)
	}
}

func TestGenericResultsKeepZeroValues(t *testing.T) {
	// Arrange
	s := &state{}
	check := func(_ context.Context, value *state) ([]int, error) {
		value.trace = append(value.trace, "check")
		return []int{0, 7}, nil
	}
	later := func(_ context.Context, value *state) ([]int, error) {
		value.trace = append(value.trace, "unreachable")
		return nil, nil
	}
	rules := seqvalidation.Sequence(seqvalidation.Group[string, state, int](check), seqvalidation.Group[string, state, int](later))

	// Act
	found, err := rules.Validate(context.Background(), s)

	// Assert
	if err != nil || !reflect.DeepEqual(found, []int{0, 7}) || !reflect.DeepEqual(s.trace, []string{"check"}) {
		t.Fatalf("found=%v err=%v calls=%v", found, err, s.trace)
	}
}

func TestPlainRulePreservesNilViolationAndStopsSequence(t *testing.T) {
	// Arrange
	s := &state{}
	check := func(_ context.Context, value *state) ([]*appViolation, error) {
		value.trace = append(value.trace, "check")
		return []*appViolation{nil}, nil
	}
	later := func(_ context.Context, value *state) ([]*appViolation, error) {
		value.trace = append(value.trace, "unreachable")
		return nil, nil
	}
	rules := seqvalidation.Sequence(
		seqvalidation.Group[string, state, *appViolation](check),
		seqvalidation.Group[string, state, *appViolation](later),
	)

	// Act
	found, err := rules.Validate(context.Background(), s)

	// Assert
	if err != nil || len(found) != 1 || found[0] != nil {
		t.Fatalf("plain rule must preserve nil entry: found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(s.trace, []string{"check"}) {
		t.Fatalf("calls: %v", s.trace)
	}
}
