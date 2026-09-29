package checkout

import (
	"errors"
	"sync"
	"testing"
)

func TestIdempotentReplay(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 500, 10)
	e.SetItemQuantity("m1", "tok1", p.ID, 2)

	o1, err := e.Checkout("m1", "tok1", "same-key")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	o2, err := e.Checkout("m1", "tok1", "same-key")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if o1.ID != o2.ID {
		t.Fatalf("replay returned different order: %d vs %d", o1.ID, o2.ID)
	}
	got, _ := e.GetProduct("m1", p.ID)
	if got.Stock != 8 {
		t.Fatalf("stock deducted twice: %d", got.Stock)
	}
}

func TestIdempotencyKeyConflictAcrossCarts(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 500, 10)
	e.SetItemQuantity("m1", "tok1", p.ID, 1)
	e.SetItemQuantity("m1", "tok2", p.ID, 1)
	if _, err := e.Checkout("m1", "tok1", "shared"); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := e.Checkout("m1", "tok2", "shared"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("want ErrIdempotencyConflict, got %v", err)
	}
	// 键只在商户内有效：另一个商户可以复用。
	e.SetItemQuantity("m2", "tok9", mustProduct(t, e, "m2", "x", 100, 5).ID, 1)
	if _, err := e.Checkout("m2", "tok9", "shared"); err != nil {
		t.Fatalf("cross-merchant reuse should work: %v", err)
	}
}

func TestFailedCheckoutDoesNotBurnKey(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 500, 1)
	e.SetItemQuantity("m1", "tok1", p.ID, 5) // 库存不足，加购时就失败
	// 改小数量但结账时库存被买光。
	e.SetItemQuantity("m1", "tok1", p.ID, 1)
	e.SetItemQuantity("m1", "tok2", p.ID, 1)
	if _, err := e.Checkout("m1", "tok2", "other"); err != nil {
		t.Fatalf("checkout tok2: %v", err)
	}
	var stockErr *InsufficientStockError
	if _, err := e.Checkout("m1", "tok1", "retry-key"); !errors.As(err, &stockErr) {
		t.Fatalf("want InsufficientStockError, got %v", err)
	}
	// 补货后用同一个键重试，成功。
	if _, err := e.AdjustStock("m1", p.ID, 5); err != nil {
		t.Fatalf("restock: %v", err)
	}
	if _, err := e.Checkout("m1", "tok1", "retry-key"); err != nil {
		t.Fatalf("retry with same key: %v", err)
	}
}

// 同一个幂等键被并发重复提交：只能生成一张订单。
func TestConcurrentDuplicateIdempotencyKey(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 500, 100)
	e.SetItemQuantity("m1", "tok1", p.ID, 2)

	const n = 50
	orders := make([]*Order, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			orders[i], errs[i] = e.Checkout("m1", "tok1", "dup-key")
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("request %d failed: %v", i, errs[i])
		}
		if orders[i].ID != orders[0].ID {
			t.Fatalf("request %d got different order %d, want %d", i, orders[i].ID, orders[0].ID)
		}
	}
	got, _ := e.GetProduct("m1", p.ID)
	if got.Stock != 98 {
		t.Fatalf("stock deducted more than once: %d", got.Stock)
	}
}
