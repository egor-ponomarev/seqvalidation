package main

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/egor-ponomarev/seqvalidation"
)

const (
	fieldUserID   = "userId"
	fieldOrderID  = "orderId"
	fieldMethod   = "method"
	fieldAmount   = "amount"
	fieldCurrency = "currency"

	codeRequired = "required"
	codePositive = "positive"
	codeAllowed  = "unsupported"
	codeNotFound = "not_found"
	codeMismatch = "order_mismatch"

	methodPayPal     = "paypal"
	methodMastercard = "mastercard"
	currencyUSD      = "USD"
	currencyEUR      = "EUR"
)

type Violation struct {
	Path string
	Code string
}

type PayRequest struct {
	UserID   *int
	OrderID  *int
	Method   string
	Amount   *int64 // Minor currency units: 1250 means 12.50 USD or EUR.
	Currency string
}

type User struct{ ID int }

type Order struct {
	Amount   int64
	Currency string
}

type Repository interface {
	GetUser(context.Context, int) (*User, error)
	GetOrder(context.Context, int) (*Order, error)
}

// Create a new state per validation; lookup inputs must not change after loading.
type PayState struct {
	Request PayRequest
	repo    Repository

	userLoaded bool
	user       *User
	userErr    error

	orderLoaded bool
	order       *Order
	orderErr    error
}

func (s *PayState) GetUser(ctx context.Context) (*User, error) {
	// Loaded flags also cache missing entities and errors, preventing retries.
	if !s.userLoaded {
		s.user, s.userErr = s.repo.GetUser(ctx, *s.Request.UserID)
		s.userLoaded = true
	}

	return s.user, s.userErr
}

func (s *PayState) GetOrder(ctx context.Context) (*Order, error) {
	if !s.orderLoaded {
		s.order, s.orderErr = s.repo.GetOrder(ctx, *s.Request.OrderID)
		s.orderLoaded = true
	}

	return s.order, s.orderErr
}

func required(_ context.Context, field string, s *PayState) ([]*Violation, error) {
	var missing bool
	switch field {
	case fieldUserID:
		missing = s.Request.UserID == nil
	case fieldOrderID:
		missing = s.Request.OrderID == nil
	case fieldMethod:
		missing = s.Request.Method == ""
	case fieldAmount:
		missing = s.Request.Amount == nil
	case fieldCurrency:
		missing = s.Request.Currency == ""
	}

	if missing {
		return []*Violation{{Path: field, Code: codeRequired}}, nil
	}

	return nil, nil
}

func positive(_ context.Context, field string, s *PayState) ([]*Violation, error) {
	var valid bool
	switch field {
	case fieldUserID:
		valid = *s.Request.UserID > 0
	case fieldOrderID:
		valid = *s.Request.OrderID > 0
	case fieldAmount:
		valid = *s.Request.Amount > 0
	}

	if !valid {
		return []*Violation{{Path: field, Code: codePositive}}, nil
	}

	return nil, nil
}

func methodSupported(_ context.Context, field string, s *PayState) ([]*Violation, error) {
	if !slices.Contains([]string{methodPayPal, methodMastercard}, s.Request.Method) {
		return []*Violation{{Path: field, Code: codeAllowed}}, nil
	}

	return nil, nil
}

func currencySupported(_ context.Context, field string, s *PayState) ([]*Violation, error) {
	if !slices.Contains([]string{currencyUSD, currencyEUR}, s.Request.Currency) {
		return []*Violation{{Path: field, Code: codeAllowed}}, nil
	}

	return nil, nil
}

func userExists(ctx context.Context, field string, s *PayState) ([]*Violation, error) {
	user, err := s.GetUser(ctx)
	if err != nil {
		return nil, err
	}

	if user == nil {
		return []*Violation{{Path: field, Code: codeNotFound}}, nil
	}

	return nil, nil
}

func orderExists(ctx context.Context, field string, s *PayState) ([]*Violation, error) {
	order, err := s.GetOrder(ctx)
	if err != nil {
		return nil, err
	}

	if order == nil {
		return []*Violation{{Path: field, Code: codeNotFound}}, nil
	}

	return nil, nil
}

func currencyMatchesOrder(ctx context.Context, field string, s *PayState) ([]*Violation, error) {
	order, err := s.GetOrder(ctx)
	if err != nil {
		return nil, err
	}

	if order == nil {
		return []*Violation{{Path: fieldOrderID, Code: codeNotFound}}, nil
	}

	if s.Request.Currency != order.Currency {
		return []*Violation{{Path: field, Code: codeMismatch}}, nil
	}

	return nil, nil
}

func amountMatchesOrder(ctx context.Context, field string, s *PayState) ([]*Violation, error) {
	order, err := s.GetOrder(ctx)
	if err != nil {
		return nil, err
	}

	if order == nil {
		return []*Violation{{Path: fieldOrderID, Code: codeNotFound}}, nil
	}

	if *s.Request.Amount != order.Amount {
		return []*Violation{{Path: field, Code: codeMismatch}}, nil
	}

	return nil, nil
}

func paymentRules() seqvalidation.Rules[string, PayState, *Violation] {
	return seqvalidation.Sequence(
		seqvalidation.Group(
			seqvalidation.Field(fieldUserID, required),
			seqvalidation.Field(fieldOrderID, required),
			seqvalidation.Field(fieldMethod, required),
			seqvalidation.Field(fieldAmount, required),
			seqvalidation.Field(fieldCurrency, required),
		).Named(codeRequired),
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
}

type memoryRepository struct {
	users  map[int]*User
	orders map[int]*Order

	userCalls  int
	orderCalls int
	userErr    error
	orderErr   error
}

func (r *memoryRepository) GetUser(ctx context.Context, id int) (*User, error) {
	r.userCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return r.users[id], r.userErr
}

func (r *memoryRepository) GetOrder(ctx context.Context, id int) (*Order, error) {
	r.orderCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return r.orders[id], r.orderErr
}

func main() {
	rules := paymentRules()
	if err := rules.Check(); err != nil {
		panic(err)
	}

	userID, orderID, amount := 1, 42, int64(1250)
	repo := &memoryRepository{
		users:  map[int]*User{userID: {ID: userID}},
		orders: map[int]*Order{orderID: {Amount: amount, Currency: currencyUSD}},
	}
	state := &PayState{
		Request: PayRequest{UserID: &userID, OrderID: &orderID, Method: methodPayPal, Amount: &amount, Currency: currencyUSD},
		repo:    repo,
	}

	violations, err := rules.Validate(context.Background(), state)
	if err != nil {
		var execution *seqvalidation.ExecutionError
		if errors.As(err, &execution) {
			fmt.Printf("failed group: %s\n", execution.GroupName)
		}

		fmt.Println(err)
		return
	}

	fmt.Printf("violations: %d; user queries: %d; order queries: %d\n", len(violations), repo.userCalls, repo.orderCalls)
}
