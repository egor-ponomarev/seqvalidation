package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/egor-ponomarev/seqvalidation"
)

func validRequest() PayRequest {
	userID, orderID, amount := 1, 42, int64(1250)
	return PayRequest{UserID: &userID, OrderID: &orderID, Method: "paypal", Amount: &amount, Currency: "USD"}
}

func validRepository() *memoryRepository {
	return &memoryRepository{
		users:  map[int]*User{1: {ID: 1}},
		orders: map[int]*Order{42: {Amount: 1250, Currency: "USD"}},
	}
}

func TestEachInvocationLoadsUserAndOrderOnce(t *testing.T) {
	// Arrange
	repo := validRepository()
	rules := paymentRules()
	requests := []PayRequest{validRequest(), validRequest()}
	requests[1].Method = "mastercard"
	type result struct {
		violations            []*Violation
		err                   error
		userCalls, orderCalls int
	}
	results := make([]result, len(requests))

	// Act
	for i, request := range requests {
		state := &PayState{Request: request, repo: repo}
		results[i].violations, results[i].err = rules.Validate(context.Background(), state)
		results[i].userCalls, results[i].orderCalls = repo.userCalls, repo.orderCalls
	}

	// Assert
	for i, result := range results {
		if result.violations != nil || result.err != nil || result.userCalls != i+1 || result.orderCalls != i+1 {
			t.Fatalf("run %d: %+v", i, result)
		}
	}
}

func TestMissingFieldsPreventQueries(t *testing.T) {
	// Arrange
	repo := validRepository()
	state := &PayState{repo: repo}
	rules := paymentRules()
	want := []*Violation{
		{Path: "userId", Code: "required"},
		{Path: "orderId", Code: "required"},
		{Path: "method", Code: "required"},
		{Path: "amount", Code: "required"},
		{Path: "currency", Code: "required"},
	}

	// Act
	violations, err := rules.Validate(context.Background(), state)

	// Assert
	if err != nil || !reflect.DeepEqual(violations, want) || repo.userCalls != 0 || repo.orderCalls != 0 {
		t.Fatalf("violations=%v err=%v queries=%d/%d", violations, err, repo.userCalls, repo.orderCalls)
	}
}

func TestInvalidValuesPreventQueries(t *testing.T) {
	// Arrange
	userID, orderID, amount := 0, -1, int64(0)
	repo := validRepository()
	state := &PayState{Request: PayRequest{UserID: &userID, OrderID: &orderID, Method: "unknown", Amount: &amount, Currency: "unknown"}, repo: repo}
	rules := paymentRules()
	want := []*Violation{
		{Path: "userId", Code: "positive"},
		{Path: "orderId", Code: "positive"},
		{Path: "method", Code: "unsupported"},
		{Path: "amount", Code: "positive"},
		{Path: "currency", Code: "unsupported"},
	}

	// Act
	violations, err := rules.Validate(context.Background(), state)

	// Assert
	if err != nil || !reflect.DeepEqual(violations, want) || repo.userCalls != 0 || repo.orderCalls != 0 {
		t.Fatalf("violations=%v err=%v queries=%d/%d", violations, err, repo.userCalls, repo.orderCalls)
	}
}

func TestMissingEntitiesStopOrderChecks(t *testing.T) {
	tests := []struct {
		name                      string
		missingUser, missingOrder bool
		want                      []*Violation
	}{
		{"user", true, false, []*Violation{{Path: "userId", Code: "not_found"}}},
		{"order", false, true, []*Violation{{Path: "orderId", Code: "not_found"}}},
		{"both", true, true, []*Violation{{Path: "userId", Code: "not_found"}, {Path: "orderId", Code: "not_found"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			repo := validRepository()
			if tt.missingUser {
				repo.users = nil
			}
			if tt.missingOrder {
				repo.orders = nil
			}
			state := &PayState{Request: validRequest(), repo: repo}
			rules := paymentRules()

			// Act
			violations, err := rules.Validate(context.Background(), state)

			// Assert
			if err != nil || !reflect.DeepEqual(violations, tt.want) || repo.userCalls != 1 || repo.orderCalls != 1 {
				t.Fatalf("violations=%v err=%v queries=%d/%d", violations, err, repo.userCalls, repo.orderCalls)
			}
		})
	}
}

func TestOrderChecksCollectBothMismatches(t *testing.T) {
	// Arrange
	repo := validRepository()
	repo.orders[42] = &Order{Amount: 999, Currency: "EUR"}
	state := &PayState{Request: validRequest(), repo: repo}
	rules := paymentRules()
	want := []*Violation{{Path: "currency", Code: "order_mismatch"}, {Path: "amount", Code: "order_mismatch"}}

	// Act
	violations, err := rules.Validate(context.Background(), state)

	// Assert
	if err != nil || !reflect.DeepEqual(violations, want) || repo.orderCalls != 1 {
		t.Fatalf("violations=%v err=%v order queries=%d", violations, err, repo.orderCalls)
	}
}

func TestOrderLookupErrorPreservesCauseAndCoordinates(t *testing.T) {
	// Arrange
	cause := errors.New("database unavailable")
	repo := validRepository()
	repo.orderErr = cause
	state := &PayState{Request: validRequest(), repo: repo}
	rules := paymentRules()

	// Act
	violations, err := rules.Validate(context.Background(), state)

	// Assert
	var execution *seqvalidation.ExecutionError
	if violations != nil || !errors.Is(err, cause) || !errors.As(err, &execution) {
		t.Fatalf("violations=%v err=%v", violations, err)
	}
	if execution.GroupIndex != 2 || execution.GroupName != "exists" || execution.RuleIndex != 1 || repo.userCalls != 1 || repo.orderCalls != 1 {
		t.Fatalf("error=%+v queries=%d/%d", execution, repo.userCalls, repo.orderCalls)
	}
}

func TestLazyGettersCacheMissingEntitiesAndErrors(t *testing.T) {
	cause := errors.New("database unavailable")
	for _, lookupErr := range []error{nil, cause} {
		t.Run("cached", func(t *testing.T) {
			// Arrange
			repo := &memoryRepository{userErr: lookupErr, orderErr: lookupErr}
			state := &PayState{Request: validRequest(), repo: repo}
			type result struct {
				user              *User
				order             *Order
				userErr, orderErr error
			}
			results := make([]result, 3)

			// Act
			for i := range results {
				results[i].user, results[i].userErr = state.GetUser(context.Background())
				results[i].order, results[i].orderErr = state.GetOrder(context.Background())
			}

			// Assert
			for _, result := range results {
				if result.user != nil || result.order != nil || !errors.Is(result.userErr, lookupErr) || !errors.Is(result.orderErr, lookupErr) {
					t.Fatalf("result=%+v", result)
				}
			}
			if repo.userCalls != 1 || repo.orderCalls != 1 {
				t.Fatalf("queries=%d/%d", repo.userCalls, repo.orderCalls)
			}
		})
	}
}
