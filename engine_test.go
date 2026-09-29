package checkout

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// testClock 可手动推进的时钟。
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newEngine(t *testing.T, clock *testClock) *Engine {
	t.Helper()
	var opts []Option
	if clock != nil {
		opts = append(opts, WithClock(clock.Now))
	}
	e, err := Open(filepath.Join(t.TempDir(), "test.db"), opts...)
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func mustProduct(t *testing.T, e *Engine, merchant, name string, price, stock int64) *Product {
	t.Helper()
	p, err := e.CreateProduct(merchant, name, price, stock)
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	return p
}

func TestCartAndCheckoutBasics(t *testing.T) {
	e := newEngine(t, nil)
	p1 := mustProduct(t, e, "m1", "apple", 1000, 10)
	p2 := mustProduct(t, e, "m1", "pear", 2500, 5)

	if _, err := e.SetItemQuantity("m1", "tok1", p1.ID, 2); err != nil {
		t.Fatalf("add item: %v", err)
	}
	if _, err := e.SetItemQuantity("m1", "tok1", p2.ID, 1); err != nil {
		t.Fatalf("add item: %v", err)
	}
	cart, err := e.GetCart("m1", "tok1")
	if err != nil {
		t.Fatalf("get cart: %v", err)
	}
	if len(cart.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(cart.Items))
	}

	order, err := e.Checkout("m1", "tok1", "key-1")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	// 2*1000 + 1*2500 = 4500
	if order.SubtotalCents != 4500 || order.TotalCents != 4500 || order.DiscountCents != 0 {
		t.Fatalf("bad totals: %+v", order)
	}
	if len(order.Items) != 2 {
		t.Fatalf("want 2 order items, got %d", len(order.Items))
	}

	// 库存已扣。
	got, _ := e.GetProduct("m1", p1.ID)
	if got.Stock != 8 {
		t.Fatalf("want stock 8, got %d", got.Stock)
	}

	// 车已冻结：修改与再次结账都报错。
	if _, err := e.SetItemQuantity("m1", "tok1", p1.ID, 1); !errors.Is(err, ErrCartFrozen) {
		t.Fatalf("want ErrCartFrozen, got %v", err)
	}
	if _, err := e.Checkout("m1", "tok1", "key-2"); !errors.Is(err, ErrCartFrozen) {
		t.Fatalf("want ErrCartFrozen, got %v", err)
	}
}

func TestStockShortageDetails(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "rare", 100, 3)

	_, err := e.SetItemQuantity("m1", "tok1", p.ID, 5)
	var stockErr *InsufficientStockError
	if !errors.As(err, &stockErr) {
		t.Fatalf("want InsufficientStockError, got %v", err)
	}
	s := stockErr.Shortages[0]
	if s.ProductID != p.ID || s.Requested != 5 || s.Available != 3 {
		t.Fatalf("bad shortage: %+v", s)
	}
}

func TestMerchantIsolation(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 100, 10)

	// 别的商户拿不到这个商品，也不能加进自己的车。
	if _, err := e.GetProduct("m2", p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := e.SetItemQuantity("m2", "tok1", p.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// 折扣码也只在本商户内有效。
	if _, err := e.CreateDiscountCode("m1", "SAVE10", DiscountKindPercent, 10, 0, 0, 0); err != nil {
		t.Fatalf("create code: %v", err)
	}
	if _, err := e.ApplyDiscount("m2", "tok2", "SAVE10"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestAdjustStockBounds(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 100, 10)

	if _, err := e.AdjustStock("m1", p.ID, -11); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("want ErrInvalidArgument, got %v", err)
	}
	if _, err := e.AdjustStock("m1", p.ID, 999990); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("want ErrInvalidArgument, got %v", err)
	}
	got, err := e.AdjustStock("m1", p.ID, -10)
	if err != nil || got.Stock != 0 {
		t.Fatalf("want stock 0, got %v %v", got, err)
	}
}

func TestOffShelfCannotAdd(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 100, 10)
	if _, err := e.SetProductStatus("m1", p.ID, ProductStatusOffShelf); err != nil {
		t.Fatalf("off shelf: %v", err)
	}
	if _, err := e.SetItemQuantity("m1", "tok1", p.ID, 1); !errors.Is(err, ErrProductOffShelf) {
		t.Fatalf("want ErrProductOffShelf, got %v", err)
	}
}

func TestQuantityZeroRemoves(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 100, 10)
	if _, err := e.SetItemQuantity("m1", "tok1", p.ID, 3); err != nil {
		t.Fatalf("add: %v", err)
	}
	cart, err := e.SetItemQuantity("m1", "tok1", p.ID, 0)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(cart.Items) != 0 {
		t.Fatalf("want empty cart, got %+v", cart.Items)
	}
}

func TestCartKindLimit(t *testing.T) {
	e := newEngine(t, nil)
	for i := 0; i < 50; i++ {
		p := mustProduct(t, e, "m1", "p", 100, 10)
		if _, err := e.SetItemQuantity("m1", "tok1", p.ID, 1); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	extra := mustProduct(t, e, "m1", "extra", 100, 10)
	if _, err := e.SetItemQuantity("m1", "tok1", extra.ID, 1); !errors.Is(err, ErrCartFull) {
		t.Fatalf("want ErrCartFull, got %v", err)
	}
}
