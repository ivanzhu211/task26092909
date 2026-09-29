package checkout

import (
	"errors"
	"testing"
	"time"
)

func TestCartExpiryBoundary(t *testing.T) {
	clock := newTestClock()
	e := newEngine(t, clock)
	p := mustProduct(t, e, "m1", "apple", 500, 10)
	if _, err := e.SetItemQuantity("m1", "tok1", p.ID, 1); err != nil {
		t.Fatalf("add: %v", err)
	}

	// 71h59m59s：还没过期，可以改。
	clock.Advance(72*time.Hour - time.Second)
	if _, err := e.SetItemQuantity("m1", "tok1", p.ID, 2); err != nil {
		t.Fatalf("should still be mutable: %v", err)
	}

	// 再推进 1 秒，距上次修改还远不到 72h，仍然有效。
	clock.Advance(time.Second)
	if _, err := e.Checkout("m1", "tok1", "k1"); err != nil {
		t.Fatalf("checkout should succeed: %v", err)
	}
}

func TestCartExpiresAtExactly72h(t *testing.T) {
	clock := newTestClock()
	e := newEngine(t, clock)
	p := mustProduct(t, e, "m1", "apple", 500, 10)
	e.SetItemQuantity("m1", "tok1", p.ID, 1)

	// 刚好满 72 小时：过期，不能改也不能结账。
	clock.Advance(72 * time.Hour)
	if _, err := e.SetItemQuantity("m1", "tok1", p.ID, 2); !errors.Is(err, ErrCartExpired) {
		t.Fatalf("want ErrCartExpired on modify, got %v", err)
	}
	if _, err := e.ApplyDiscount("m1", "tok1", "X"); !errors.Is(err, ErrCartExpired) {
		t.Fatalf("want ErrCartExpired on discount, got %v", err)
	}
	if _, err := e.Checkout("m1", "tok1", "k1"); !errors.Is(err, ErrCartExpired) {
		t.Fatalf("want ErrCartExpired on checkout, got %v", err)
	}
	// 库存没有被扣。
	got, _ := e.GetProduct("m1", p.ID)
	if got.Stock != 10 {
		t.Fatalf("expired checkout changed stock: %d", got.Stock)
	}
}

func TestExpiryCheckoutBoundaryJustBefore(t *testing.T) {
	clock := newTestClock()
	e := newEngine(t, clock)
	p := mustProduct(t, e, "m1", "apple", 500, 10)
	e.SetItemQuantity("m1", "tok1", p.ID, 1)

	// 差 1 秒满 72 小时：还能结账。
	clock.Advance(72*time.Hour - time.Second)
	if _, err := e.Checkout("m1", "tok1", "k1"); err != nil {
		t.Fatalf("checkout just before expiry should succeed: %v", err)
	}
}
