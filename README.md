# seqvalidation

**Complex request validation, one group at a time.**

`seqvalidation` helps you move request checks out of handlers and service methods
into a reusable sequence of ordinary Go functions. Collect every violation in a
group, then continue only when that group passes. An operational error aborts the
run immediately.

Consider a payment request. The user and order IDs must be present before you
look them up. The method must be supported and the amount positive. Once the
order exists, the payment's currency and amount must match it. These checks have
dependencies: a missing order ID should never lead to a database lookup, and a
missing order should never reach a currency comparison.

Put that order in the validation definition. Your business handler can focus on
processing requests that passed the configured checks, instead of carrying a
chain of validation branches and repeated lookups.

- **Make dependencies explicit.** Required fields first, then values, lookups,
  and checks involving several fields or loaded entities.
- **Return useful errors together.** Collect all violations in the first failing
  group, so a caller can fix several related fields in one attempt.
- **Keep your own types.** Reuse existing checks, error structs, and injected
  repositories. Share one typed state across rules and cache entity lookups there.

Go 1.22+. No external dependencies or reflection.

## What it looks like

Here is the configuration from the [payment example](examples/payment/main.go).
The check functions are defined in that file; group names are optional labels
for diagnostics.

```go
rules := seqvalidation.Sequence(
    seqvalidation.Group(
        seqvalidation.Field(fieldUserID, required),
        seqvalidation.Field(fieldOrderID, required),
        seqvalidation.Field(fieldMethod, required),
        seqvalidation.Field(fieldAmount, required),
        seqvalidation.Field(fieldCurrency, required),
    ).Named("required"),
    seqvalidation.Group(
        seqvalidation.Field(fieldUserID, positive),
        seqvalidation.Field(fieldOrderID, positive),
        seqvalidation.Field(fieldMethod, methodSupported),
        seqvalidation.Field(fieldAmount, positive),
        seqvalidation.Field(fieldCurrency, currencySupported),
    ).Named("values"),
    seqvalidation.Group(
        seqvalidation.Field(fieldUserID, userExists),
        seqvalidation.Field(fieldOrderID, orderExists),
    ).Named("exists"),
    seqvalidation.Group(
        seqvalidation.Field(fieldCurrency, currencyMatchesOrder),
        seqvalidation.Field(fieldAmount, amountMatchesOrder),
    ).Named("order"),
)
```

No repository query runs if a required field or value is invalid. The existence,
currency, and amount checks share the same cached order: one order query per
validation run.

## Keep validation at the request boundary

Decode the request, create a fresh validation state, and validate before calling
the business handler. This can live in middleware, a controller's initial step,
or a service entry point. The library is independent of the web framework;
your application decides how to report invalid input and operational errors.

For example, this wrapper uses the types from the payment example:

```go
func validateAndPay(
    ctx context.Context,
    rules seqvalidation.Rules[string, PayState, *Violation],
    state *PayState,
    pay func(context.Context, PayRequest) error,
) ([]*Violation, error) {
    violations, err := rules.Validate(ctx, state)
    if err != nil {
        return nil, err
    }
    if len(violations) > 0 {
        return violations, nil
    }

    return nil, pay(ctx, state.Request)
}
```

The business function runs only on `nil, nil` from validation. The branches at
this boundary stay small as the number of checks grows; each new check goes in
the sequence. A successful run means the configured checks passed for the data
observed during that run. Enforce invariants involving changing database state
at the write boundary too.

## Where the idea comes from

The inspiration is Jakarta Bean Validation's
[group sequences](https://jakarta.ee/specifications/bean-validation/3.0/jakarta-bean-validation-spec-3.0.html#constraintdeclarationvalidationprocess-groupsequence),
implemented by
[Hibernate Validator](https://docs.hibernate.org/validator/8.0/reference/en-US/html_single/#section-defining-group-sequences).
The useful idea is simple: establish basic validity before running dependent or
expensive checks, and stop before the next group if the current one fails.

In Go, `Sequence(Group(...), Group(...))` expresses that pattern with ordinary
functions. You define the checks and call validation explicitly at your request
boundary.

## Quick start

After the first `v0.1.0` release is published, install it with:

```sh
go get github.com/egor-ponomarev/seqvalidation@v0.1.0
```

```go
package main

import (
    "context"
    "fmt"

    "github.com/egor-ponomarev/seqvalidation"
)

type Violation struct {
    Path string
    Code string
}

type PayRequest struct {
    UserID *int
    Method string
}

func userRequired(_ context.Context, field string, r *PayRequest) ([]*Violation, error) {
    if r.UserID == nil {
        return []*Violation{{Path: field, Code: "required"}}, nil
    }
    return nil, nil
}

func methodSupported(_ context.Context, field string, r *PayRequest) ([]*Violation, error) {
    if r.Method != "paypal" && r.Method != "mastercard" {
        return []*Violation{{Path: field, Code: "unsupported"}}, nil
    }
    return nil, nil
}

func main() {
    rules := seqvalidation.Sequence(
        seqvalidation.Group(seqvalidation.Field("userId", userRequired)).Named("required"),
        seqvalidation.Group(seqvalidation.Field("method", methodSupported)),
    )
    if err := rules.Check(); err != nil {
        panic(err)
    }

    violations, err := rules.Validate(context.Background(), &PayRequest{Method: "paypal"})
    if err != nil {
        fmt.Println("validation could not complete:", err)
        return
    }
    for _, violation := range violations {
        fmt.Printf("%s: %s\n", violation.Path, violation.Code)
    }
    // Output: userId: required
}
```

`Group` returns a `Stage[F, T, V]`; `Sequence` returns a `Rules[F, T, V]`. The type parameters are field identifier `F`, state `T`, and violation `V`.
`Field` lets the compiler infer all three. Groups and sequences require the same
`F`, `T`, and `V`; different named field types cannot be mixed, even when they
have the same underlying type. Plain callbacks without a field argument require
explicit `Group[F, T, V](callback)` or a `Rule[F, T, V]` conversion because their
signatures do not carry `F`.
Methods on services with injected repositories work as rules too.

## Lazy loading: payment validation

The [complete payment example](examples/payment/main.go) validates a `PayRequest`
with `UserID *int`, `OrderID *int`, `Method string`, `Amount *int64`, and
`Currency string`. Pointers distinguish missing IDs and amounts from zero.
Amounts use integer minor units: `1250` means 12.50 USD or EUR in this example.
The example accepts `paypal` and `mastercard`, and USD and EUR.

Groups run in this order:

1. Required fields: collect all missing IDs, method, amount, and currency.
2. Values: IDs and amount must be positive; method and currency must be supported.
3. Existence: load the user and order, collecting not-found violations.
4. Order checks: payment currency and amount must match the order.

Invalid required fields or values stop validation before any repository query.
A missing order stops the dependent order checks. Each group collects all its
violations; a repository error aborts immediately with the original cause.

Rules receive both `context.Context` and `*PayState`. Implement lazy getters on
application-owned state. `GetOrder` is called only after `OrderID` is validated:

```go
func (s *PayState) GetOrder(ctx context.Context) (*Order, error) {
    if !s.orderLoaded {
        s.order, s.orderErr = s.repo.GetOrder(ctx, *s.Request.OrderID)
        s.orderLoaded = true
    }
    return s.order, s.orderErr
}
```

`orderExists`, `currencyMatchesOrder`, and `amountMatchesOrder` all call
`GetOrder`, but only the first call queries the repository. The loaded flag caches
success, missing entities, and errors; there are no implicit retries during a run.
The example's `GetUser` follows the same pattern.

Construct a **fresh state for every invocation**, with repositories injected by
the application, and reuse the rules. Do not change lookup inputs after loading.
The Rules value retains no state, cache, or violations. This simple cache is for
sequential access; do not share one state between independent goroutines.
Concurrent calls are safe with independent states and concurrency-safe shared
dependencies; closures must not capture mutable request state.

## Errors and diagnostics

Handle `error` before inspecting violations. The example's `Violation` carries
a path and code. Your application can add a message, parameters, or other fields;
the library does not interpret or translate them.

Operational errors are wrapped with zero-based group and rule indexes, and the
optional group name:

```text
seqvalidation: group 2 ("exists") rule 1: database unavailable
```

```go
var execution *seqvalidation.ExecutionError
if errors.As(err, &execution) {
    logger.Error("validation failed",
        "group", execution.GroupName,
        "group_index", execution.GroupIndex,
        "rule_index", execution.RuleIndex,
        "error", execution.Err,
    )
}
```

`errors.Is` and `errors.As` reach the original cause. Configuration errors use
`*seqvalidation.ConfigError`. Violations keep their application paths and codes;
group metadata is attached to execution errors, not to individual violations.
The library does not log or install a logger.

## Existing validators

Rules return your application's violation type. The engine never reads violation
fields or converts results; the `Violation` struct above belongs to the example. All rules in
one `Rules` value share the same field identifier, state, and violation types
(`F`, `T`, `V`).

For an existing check with signature
`func(context.Context, Field, *State) ([]*Violation, error)`, use `Field`:

```go
rules := seqvalidation.Sequence(
    seqvalidation.Group(
        seqvalidation.Field(fieldUserID, required),
        seqvalidation.Field(fieldOrderID, required),
    ),
    seqvalidation.Group(
        seqvalidation.Field(fieldOrderID, orderExists),
    ),
)
violations, err := rules.Validate(ctx, state) // []*Violation, error
```

`Field` binds the field argument. A nil or empty result slice means success;
nil entries are skipped. It preserves the
original error pointers, order, and parameters without modifying the returned
slice. A nil check is rejected by configuration preflight. Plain rules return `[]V`
directly; every element counts as a violation, including zero values and nil
pointers. Nil filtering belongs to `Field`, not to the generic engine.
Dependencies and lazy state getters stay in the application. Another validation
library can still be called inside a rule.

## Execution contract

- Groups and their rules execute sequentially in declaration order.
- Every rule in a group runs even if a previous rule returned violations.
- The first group with violations ends the sequence; its violations are returned.
- A rule's operational error immediately aborts the run and returns `nil, error`.
  Already collected violations are discarded, including any returned with that error.
- Success returns `nil, nil`.
- Adding the same rule twice executes it twice. Names do not select or deduplicate rules.
- Changes to state are not rolled back after violations, errors, or cancellation.
- Empty sequences, empty groups, and nil rules are configuration errors. `Check`
  detects them without invoking rules. `Validate` also checks the entire configuration before
  executing any rule, including when a later group is invalid.
- Nil contexts and states return `ErrNilContext` and `ErrNilState` respectively.
- Cancellation is checked before and after every rule; rule errors take precedence
  if a rule returns an error while the context is canceled. Pass context through to
  your repositories. The library cannot interrupt a rule that ignores context.
- Application panics are not recovered.

There is no separate schema, group registry, reflection, global mutable state, or
automatic parallelism. Rules also run in declaration order within each group.
The library does not implement Bean Validation annotations, group inheritance,
constraint deduplication, or automatic Spring-style request interception.
For conditional checks, use an ordinary `if` inside a rule; it sees the current
state at execution time.

## Run locally

```sh
make check
go run ./examples/payment
```

`make check` runs formatting, tests on Go 1.22.12, race tests and vet on
Go 1.27.1, golangci-lint 2.14.0, and govulncheck v1.8.0. Exact versions live in
`Makefile`. Development tooling requires Go, make, curl, and standard shell
utilities; the first run downloads toolchains and tools. `govulncheck` also queries
the vulnerability database. Tool binaries are stored under ignored `.tools/`;
the application module gains no dependencies.

Use individual targets (`make test`, `make test-min`, `make test-race`, `make lint`)
while iterating.

[GitHub Actions](.github/workflows/ci.yml) runs the same `make check` on every push
and pull request, and can also be started manually. Required checks and approving
reviews must be configured separately in GitHub branch protection settings.

The example prints:

```text
violations: 0; user queries: 1; order queries: 1
```

For local development against this checkout, keep the same module path and add
a replacement in the consuming project:

```sh
go mod edit -require=github.com/egor-ponomarev/seqvalidation@v0.0.0
go mod edit -replace=github.com/egor-ponomarev/seqvalidation=/absolute/path/to/seqvalidation
go mod tidy
```

Import `"github.com/egor-ponomarev/seqvalidation"`. Remove the local replacement
when switching to a published version. The `go` directive declares the minimum
toolchain version; using a newer toolchain does not require raising that minimum.

## Contributing

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) for
commit messages and pull request titles:

```text
feat: add named validation groups
fix: preserve rule errors when context is canceled
docs: clarify request-scoped state
ci: update the Go toolchain
```

The format is `type(optional-scope): description`. Use `feat` for new features,
`fix` for bug fixes, and `docs`, `ci`, `test`, `refactor`, `perf`, `build`, `chore`,
or `revert` for other changes. Scope is optional, for example
`fix(validation): preserve rule errors`. Mark breaking changes with `!`, as in
`feat!: change the rule signature`, or a `BREAKING CHANGE:` footer in a commit.
PR titles must use `!` because they have no footer.

[PR title validation](.github/workflows/pr-title.yml) checks titles when a PR is
opened, edited, reopened, or updated. It does not validate individual commits or
direct pushes. Use **Squash and merge** and configure GitHub's default squash
commit message to use the PR title. Make `Validate PR title` a required check in
branch protection if you want GitHub to enforce it before merging.
