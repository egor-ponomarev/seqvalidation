package seqvalidation_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/egor-ponomarev/seqvalidation"
)

type violation struct {
	Path    string
	Code    string
	Message string
	Params  map[string]any
}

type state struct {
	trace []string
	value int
}

func record(name string, codes ...string) seqvalidation.Rule[string, state, violation] {
	return func(_ context.Context, s *state) ([]violation, error) {
		s.trace = append(s.trace, name)
		var violations []violation
		for _, code := range codes {
			violations = append(violations, violation{Path: name, Code: code})
		}
		return violations, nil
	}
}

func TestGroupCollectsAllViolationsAndStopsSequence(t *testing.T) {
	// Arrange
	rules := seqvalidation.Sequence(
		seqvalidation.Group[string, state, violation](record("first", "required", "format"), record("second", "size")),
		seqvalidation.Group[string, state, violation](record("unreachable")),
	)
	s := &state{}
	want := []violation{
		{Path: "first", Code: "required"},
		{Path: "first", Code: "format"},
		{Path: "second", Code: "size"},
	}

	// Act
	got, err := rules.Validate(context.Background(), s)

	// Assert
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate = %#v, %v; want %#v, nil", got, err, want)
	}
	if !reflect.DeepEqual(s.trace, []string{"first", "second"}) {
		t.Fatalf("unexpected execution: %v", s.trace)
	}
}

func TestStateFlowsAcrossGroupsAndRepeatedRulesRun(t *testing.T) {
	// Arrange
	increment := func(_ context.Context, s *state) ([]violation, error) {
		s.value++
		return nil, nil
	}
	observed := -1
	read := func(_ context.Context, s *state) ([]violation, error) {
		observed = s.value
		return nil, nil
	}
	rules := seqvalidation.Sequence(
		seqvalidation.Group[string, state, violation](increment),
		seqvalidation.Group[string, state, violation](increment, read),
	)
	s := &state{}

	// Act
	got, err := rules.Validate(context.Background(), s)

	// Assert
	if got != nil || err != nil {
		t.Fatalf("Validate = %v, %v", got, err)
	}
	if observed != 2 || s.value != 2 {
		t.Fatalf("state did not flow across groups: observed=%d final=%d", observed, s.value)
	}
}

type databaseError struct{ message string }

func (e *databaseError) Error() string { return e.message }

func TestOperationalErrorDiscardsViolationsAndPreservesCause(t *testing.T) {
	// Arrange
	cause := &databaseError{message: "database unavailable"}
	fail := func(_ context.Context, s *state) ([]violation, error) {
		s.trace = append(s.trace, "failed")
		s.value = 42
		return []violation{{Code: "discard_me"}}, cause
	}
	rules := seqvalidation.Sequence(
		seqvalidation.Group[string, state, violation](record("earlier", "invalid"), fail, record("unreachable")).Named("business"),
		seqvalidation.Group[string, state, violation](record("later")),
	)
	s := &state{}

	// Act
	got, err := rules.Validate(context.Background(), s)

	// Assert
	var location *seqvalidation.ExecutionError
	var underlying *databaseError
	if got != nil || !errors.Is(err, cause) || !errors.As(err, &underlying) || !errors.As(err, &location) {
		t.Fatalf("Validate = %v, %v; expected wrapped cause and no violations", got, err)
	}
	if location.GroupIndex != 0 || location.RuleIndex != 1 || location.GroupName != "business" {
		t.Fatalf("wrong error location: %+v", location)
	}
	if !strings.Contains(err.Error(), "business") || !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("unhelpful error: %v", err)
	}
	if s.value != 42 || !reflect.DeepEqual(s.trace, []string{"earlier", "failed"}) {
		t.Fatalf("state rolled back or later rule ran: %+v", s)
	}
}

func TestConfigurationIsCheckedBeforeAnyExecution(t *testing.T) {
	tests := []struct {
		name    string
		rules   seqvalidation.Rules[string, state, violation]
		want    seqvalidation.ConfigError
		message string
	}{
		{
			name:    "zero",
			rules:   seqvalidation.Rules[string, state, violation]{},
			want:    seqvalidation.ConfigError{GroupIndex: -1, RuleIndex: -1, Reason: "empty sequence"},
			message: "seqvalidation: empty sequence",
		},
		{
			name:    "empty_sequence",
			rules:   seqvalidation.Sequence[string, state, violation](),
			want:    seqvalidation.ConfigError{GroupIndex: -1, RuleIndex: -1, Reason: "empty sequence"},
			message: "seqvalidation: empty sequence",
		},
		{
			name:    "empty_first_group",
			rules:   seqvalidation.Sequence(seqvalidation.Group[string, state, violation]()),
			want:    seqvalidation.ConfigError{GroupIndex: 0, RuleIndex: -1, Reason: "empty group"},
			message: "seqvalidation: group 0: empty group",
		},
		{
			name:    "empty_later_named_group",
			rules:   seqvalidation.Sequence(seqvalidation.Group[string, state, violation](record("must_not_run")), seqvalidation.Group[string, state, violation]().Named("empty")),
			want:    seqvalidation.ConfigError{GroupIndex: 1, GroupName: "empty", RuleIndex: -1, Reason: "empty group"},
			message: "seqvalidation: group 1 (\"empty\"): empty group",
		},
		{
			name:    "nil_first_rule",
			rules:   seqvalidation.Sequence(seqvalidation.Group[string, state, violation](nil)),
			want:    seqvalidation.ConfigError{GroupIndex: 0, RuleIndex: 0, Reason: "nil rule"},
			message: "seqvalidation: group 0 rule 0: nil rule",
		},
		{
			name: "nil_later_rule_in_third_group",
			rules: seqvalidation.Sequence(
				seqvalidation.Group[string, state, violation](record("first", "invalid")),
				seqvalidation.Group[string, state, violation](record("second")),
				seqvalidation.Group[string, state, violation](record("third"), nil).Named("broken"),
			),
			want:    seqvalidation.ConfigError{GroupIndex: 2, GroupName: "broken", RuleIndex: 1, Reason: "nil rule"},
			message: "seqvalidation: group 2 (\"broken\") rule 1: nil rule",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			s := &state{}

			// Act
			checkErr := tt.rules.Check()
			got, err := tt.rules.Validate(context.Background(), s)

			// Assert
			if got != nil || len(s.trace) != 0 {
				t.Fatalf("Validate = %v, %v; trace %v", got, err, s.trace)
			}
			for _, result := range []struct {
				method string
				err    error
			}{{"Check", checkErr}, {"Validate", err}} {
				var config *seqvalidation.ConfigError
				if !errors.As(result.err, &config) {
					t.Fatalf("%s = %v; want ConfigError", result.method, result.err)
				}
				if *config != tt.want {
					t.Errorf("%s error = %+v; want %+v", result.method, *config, tt.want)
				}
				if config.Error() != tt.message {
					t.Errorf("%s message = %q; want %q", result.method, config.Error(), tt.message)
				}
			}
		})
	}
}

func TestRuleErrorTakesPrecedenceOverCancellation(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cause := errors.New("repository failure")
	fail := func(_ context.Context, s *state) ([]violation, error) {
		s.trace = append(s.trace, "failed")
		cancel()
		return []violation{{Code: "discard_me"}}, cause
	}
	rules := seqvalidation.Sequence(
		seqvalidation.Group[string, state, violation](record("earlier", "invalid"), fail, record("unreachable")),
		seqvalidation.Group[string, state, violation](record("later")),
	)
	s := &state{}

	// Act
	violations, err := rules.Validate(ctx, s)

	// Assert
	if violations != nil || !errors.Is(err, cause) || errors.Is(err, context.Canceled) {
		t.Fatalf("Validate = %v, %v; want nil, wrapped rule error", violations, err)
	}
	if !reflect.DeepEqual(s.trace, []string{"earlier", "failed"}) {
		t.Fatalf("unexpected execution: %v", s.trace)
	}
}

func TestExecutionErrorCoordinatesInLaterGroup(t *testing.T) {
	for _, name := range []string{"", "business"} {
		t.Run(fmt.Sprintf("group_name=%q", name), func(t *testing.T) {
			// Arrange
			cause := errors.New("repository failure")
			fail := func(_ context.Context, s *state) ([]violation, error) {
				s.trace = append(s.trace, "failed")
				return nil, cause
			}
			rules := seqvalidation.Sequence(
				seqvalidation.Group[string, state, violation](record("first")),
				seqvalidation.Group[string, state, violation](record("second")),
				seqvalidation.Group[string, state, violation](record("third"), fail, record("unreachable")).Named(name),
				seqvalidation.Group[string, state, violation](record("later")),
			)
			s := &state{}
			wantMessage := "seqvalidation: group 2 rule 1: repository failure"
			if name != "" {
				wantMessage = "seqvalidation: group 2 (\"business\") rule 1: repository failure"
			}

			// Act
			violations, err := rules.Validate(context.Background(), s)

			// Assert
			var execution *seqvalidation.ExecutionError
			if violations != nil || !errors.Is(err, cause) || !errors.As(err, &execution) {
				t.Fatalf("Validate = %v, %v; want nil, wrapped rule error", violations, err)
			}
			if execution.GroupIndex != 2 || execution.GroupName != name || execution.RuleIndex != 1 {
				t.Fatalf("wrong execution coordinates: %+v", execution)
			}
			if err.Error() != wantMessage {
				t.Errorf("message = %q; want %q", err.Error(), wantMessage)
			}
			if !reflect.DeepEqual(s.trace, []string{"first", "second", "third", "failed"}) {
				t.Errorf("unexpected execution: %v", s.trace)
			}
		})
	}
}

func TestNilArguments(t *testing.T) {
	tests := []struct {
		name  string
		ctx   context.Context
		state *state
		want  error
	}{
		{"nil_context", nil, &state{}, seqvalidation.ErrNilContext},
		{"nil_state", context.Background(), nil, seqvalidation.ErrNilState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			rules := seqvalidation.Sequence(seqvalidation.Group[string, state, violation](record("never")))

			// Act
			got, err := rules.Validate(tt.ctx, tt.state)

			// Assert
			if got != nil || !errors.Is(err, tt.want) {
				t.Fatalf("Validate = %v, %v; want nil, %v", got, err, tt.want)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	t.Run("before_first_rule", func(t *testing.T) {
		// Arrange
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := &state{}
		rules := seqvalidation.Sequence(seqvalidation.Group[string, state, violation](record("never")))

		// Act
		got, err := rules.Validate(ctx, s)

		// Assert
		if got != nil || !errors.Is(err, context.Canceled) || len(s.trace) != 0 {
			t.Fatalf("Validate = %v, %v; trace %v", got, err, s.trace)
		}
	})
	t.Run("during_final_rule", func(t *testing.T) {
		// Arrange
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rule := func(context.Context, *state) ([]violation, error) {
			cancel()
			return []violation{{Code: "discard_me"}}, nil
		}
		rules := seqvalidation.Sequence(seqvalidation.Group[string, state, violation](rule))
		s := &state{}

		// Act
		got, err := rules.Validate(ctx, s)

		// Assert
		if got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Validate = %v, %v", got, err)
		}
	})
	t.Run("during_rule_stops_remaining_rules", func(t *testing.T) {
		// Arrange
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rule := func(context.Context, *state) ([]violation, error) {
			cancel()
			return nil, nil
		}
		s := &state{}
		rules := seqvalidation.Sequence(seqvalidation.Group[string, state, violation](rule, record("never")))

		// Act
		got, err := rules.Validate(ctx, s)

		// Assert
		if got != nil || !errors.Is(err, context.Canceled) || len(s.trace) != 0 {
			t.Fatalf("Validate = %v, %v; trace %v", got, err, s.trace)
		}
	})
}

func TestGroupSnapshotsRules(t *testing.T) {
	// Arrange
	callbacks := []seqvalidation.Rule[string, state, violation]{record("original")}
	group := seqvalidation.Group[string, state, violation](callbacks...)
	callbacks[0] = nil
	rules := seqvalidation.Sequence(group)
	s := &state{}

	// Act
	got, err := rules.Validate(context.Background(), s)

	// Assert
	if got != nil || err != nil || !reflect.DeepEqual(s.trace, []string{"original"}) {
		t.Fatalf("configuration changed: %v, %v; trace %v", got, err, s.trace)
	}
}

func TestSequenceSnapshotsGroups(t *testing.T) {
	// Arrange
	groups := []seqvalidation.Stage[string, state, violation]{seqvalidation.Group[string, state, violation](record("original"))}
	rules := seqvalidation.Sequence(groups...)
	groups[0] = seqvalidation.Group[string, state, violation](nil)
	s := &state{}

	// Act
	got, err := rules.Validate(context.Background(), s)

	// Assert
	if got != nil || err != nil || !reflect.DeepEqual(s.trace, []string{"original"}) {
		t.Fatalf("configuration changed: %v, %v; trace %v", got, err, s.trace)
	}
}

func TestNamedReturnsIndependentCopy(t *testing.T) {
	cause := errors.New("failure")
	fail := func(context.Context, *state) ([]violation, error) { return nil, cause }
	original := seqvalidation.Group[string, state, violation](fail)
	named := original.Named("copy")
	tests := []struct {
		name  string
		stage seqvalidation.Stage[string, state, violation]
		want  string
	}{
		{"original", original, ""},
		{"named_copy", named, "copy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			rules := seqvalidation.Sequence(tt.stage)
			s := &state{}

			// Act
			_, err := rules.Validate(context.Background(), s)

			// Assert
			var location *seqvalidation.ExecutionError
			if !errors.As(err, &location) || location.GroupName != tt.want {
				t.Fatalf("unexpected group name: %v", err)
			}
		})
	}
}

func TestConcurrentAndRepeatedRulesReuse(t *testing.T) {
	// Arrange
	const workers, runs = 32, 3
	rules := seqvalidation.Sequence(seqvalidation.Group[string, state, violation](record("one")), seqvalidation.Group[string, state, violation](record("two")))
	type result struct {
		state      *state
		violations []violation
		err        error
	}
	results := make([]result, workers*runs)
	for i := range results {
		results[i].state = &state{}
	}
	var wg sync.WaitGroup

	// Act
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for run := 0; run < runs; run++ {
				r := &results[worker*runs+run]
				r.violations, r.err = rules.Validate(context.Background(), r.state)
			}
		}(worker)
	}
	wg.Wait()

	// Assert
	for i, r := range results {
		if r.violations != nil || r.err != nil || !reflect.DeepEqual(r.state.trace, []string{"one", "two"}) {
			t.Errorf("run %d: shared invocation state: %v, %v, %v", i, r.violations, r.err, r.state.trace)
		}
	}
}

func ExampleSequence() {
	// Arrange
	type PayRequest struct{ UserID *int }
	type Violation struct{ Path, Code string }
	required := func(_ context.Context, r *PayRequest) ([]Violation, error) {
		if r.UserID == nil {
			return []Violation{{Path: "userId", Code: "required"}}, nil
		}
		return nil, nil
	}
	rules := seqvalidation.Sequence(seqvalidation.Group[string, PayRequest, Violation](required))

	// Act
	violations, err := rules.Validate(context.Background(), &PayRequest{})

	// Assert
	fmt.Println(violations, err)
	// Output: [{userId required}] <nil>
}

func ExampleField() {
	// Arrange
	type PayRequest struct{ UserID *int }
	type Violation struct{ Path, Code string }
	required := func(_ context.Context, field string, r *PayRequest) ([]*Violation, error) {
		var found *Violation
		if r.UserID == nil {
			found = &Violation{Path: field, Code: "required"}
		}
		return []*Violation{found}, nil
	}
	rules := seqvalidation.Sequence(seqvalidation.Group(seqvalidation.Field("userId", required)))
	userID := 1

	// Act
	valid, validErr := rules.Validate(context.Background(), &PayRequest{UserID: &userID})
	invalid, invalidErr := rules.Validate(context.Background(), &PayRequest{})

	// Assert
	fmt.Println(len(valid), validErr)
	fmt.Println(invalid[0].Path, invalid[0].Code, invalidErr)
	// Output:
	// 0 <nil>
	// userId required <nil>
}
