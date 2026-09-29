package checkout

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeClock 可手动推进的时钟。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestExpiryBoundary(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	e := openTest(t, WithClock(clock.Now))
	pid := mustProduct(t, e, "m1", "a", 100, 10)

	e.SetItemQuantity("m1", "t", pid, 1)

	// 71h59m59s：未过期，可修改。
	clock.Advance(CartTTL - time.Second)
	if err := e.SetItemQuantity("m1", "t", pid, 2); err != nil {
		t.Fatalf("should be mutable before TTL: %v", err)
	}
	// 上面那次修改刷新了最后修改时间，重新计 72h。
	clock.Advance(CartTTL - time.Nanosecond)
	if err := e.ApplyDiscount("m1", "t", ""); err != nil {
		t.Fatalf("1ns before TTL should still be mutable: %v", err)
	}
	if _, err := e.Checkout("m1", "t", "k-early"); err != nil {
		t.Fatalf("checkout 1ns before TTL should succeed: %v", err)
	}

	// 恰好满 72 小时：过期，不能修改也不能结账。
	e.SetItemQuantity("m1", "t2", pid, 1)
	clock.Advance(CartTTL)
	if err := e.SetItemQuantity("m1", "t2", pid, 2); !errors.Is(err, ErrCartExpired) {
		t.Fatalf("at TTL want ErrCartExpired on modify, got %v", err)
	}
	if err := e.ApplyDiscount("m1", "t2", ""); !errors.Is(err, ErrCartExpired) {
		t.Fatalf("at TTL want ErrCartExpired on discount, got %v", err)
	}
	if _, err := e.Checkout("m1", "t2", "k-late"); !errors.Is(err, ErrCartExpired) {
		t.Fatalf("at TTL want ErrCartExpired on checkout, got %v", err)
	}

	// 超过 72h 后同样过期。
	clock.Advance(time.Hour)
	if _, err := e.Checkout("m1", "t2", "k-late2"); !errors.Is(err, ErrCartExpired) {
		t.Fatalf("after TTL want ErrCartExpired, got %v", err)
	}
}
