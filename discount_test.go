package checkout

import (
	"errors"
	"testing"
)

func TestPercentDiscountRounding(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 333, 10) // 333*3=999, 10% -> 99.9 -> 100
	if _, err := e.CreateDiscountCode("m1", "P10", DiscountKindPercent, 10, 0, 0, 0); err != nil {
		t.Fatalf("create code: %v", err)
	}
	if _, err := e.SetItemQuantity("m1", "tok1", p.ID, 3); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := e.ApplyDiscount("m1", "tok1", "P10"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	order, err := e.Checkout("m1", "tok1", "k1")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.SubtotalCents != 999 || order.DiscountCents != 100 || order.TotalCents != 899 {
		t.Fatalf("bad totals: %+v", order)
	}
}

func TestFixedDiscountFloorsAtZero(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 500, 10)
	if _, err := e.CreateDiscountCode("m1", "F1000", DiscountKindFixed, 0, 1000, 0, 0); err != nil {
		t.Fatalf("create code: %v", err)
	}
	e.SetItemQuantity("m1", "tok1", p.ID, 1)
	e.ApplyDiscount("m1", "tok1", "F1000")
	order, err := e.Checkout("m1", "tok1", "k1")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.DiscountCents != 500 || order.TotalCents != 0 {
		t.Fatalf("bad totals: %+v", order)
	}
}

func TestDiscountMinSubtotal(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 500, 10)
	if _, err := e.CreateDiscountCode("m1", "MIN", DiscountKindPercent, 10, 0, 1000, 0); err != nil {
		t.Fatalf("create code: %v", err)
	}
	e.SetItemQuantity("m1", "tok1", p.ID, 1) // 小计 500 < 1000
	e.ApplyDiscount("m1", "tok1", "MIN")
	if _, err := e.Checkout("m1", "tok1", "k1"); !errors.Is(err, ErrDiscountMinSubtotal) {
		t.Fatalf("want ErrDiscountMinSubtotal, got %v", err)
	}
	// 失败的结账不留下任何改动：库存没扣，车还能改。
	got, _ := e.GetProduct("m1", p.ID)
	if got.Stock != 10 {
		t.Fatalf("stock changed after failed checkout: %d", got.Stock)
	}
	e.SetItemQuantity("m1", "tok1", p.ID, 2) // 小计 1000，达到门槛
	order, err := e.Checkout("m1", "tok1", "k1")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if order.DiscountCents != 100 || order.TotalCents != 900 {
		t.Fatalf("bad totals: %+v", order)
	}
}

func TestDiscountUsageLimit(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 500, 100)
	if _, err := e.CreateDiscountCode("m1", "LIM2", DiscountKindPercent, 10, 0, 0, 2); err != nil {
		t.Fatalf("create code: %v", err)
	}
	for i, tok := range []string{"t1", "t2", "t3"} {
		e.SetItemQuantity("m1", tok, p.ID, 1)
		e.ApplyDiscount("m1", tok, "LIM2")
		_, err := e.Checkout("m1", tok, "k"+tok)
		if i < 2 && err != nil {
			t.Fatalf("checkout %d: %v", i, err)
		}
		if i == 2 && !errors.Is(err, ErrDiscountExhausted) {
			t.Fatalf("want ErrDiscountExhausted, got %v", err)
		}
	}
	// 取消一张订单后次数被归还，可以再用。
	o1, err := e.GetOrder("m1", 1)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if _, err := e.CancelOrder("m1", o1.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := e.Checkout("m1", "t3", "kt3"); err != nil {
		t.Fatalf("checkout after cancel: %v", err)
	}
}

func TestCancelRestoresStockOnce(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 500, 10)
	e.SetItemQuantity("m1", "tok1", p.ID, 4)
	order, err := e.Checkout("m1", "tok1", "k1")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	got, _ := e.GetProduct("m1", p.ID)
	if got.Stock != 6 {
		t.Fatalf("want stock 6, got %d", got.Stock)
	}
	canceled, err := e.CancelOrder("m1", order.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != OrderStatusCanceled {
		t.Fatalf("want canceled, got %v", canceled.Status)
	}
	got, _ = e.GetProduct("m1", p.ID)
	if got.Stock != 10 {
		t.Fatalf("want stock restored to 10, got %d", got.Stock)
	}
	// 重复取消不重复归还。
	if _, err := e.CancelOrder("m1", order.ID); err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	got, _ = e.GetProduct("m1", p.ID)
	if got.Stock != 10 {
		t.Fatalf("double cancel changed stock: %d", got.Stock)
	}
}

func TestPriceSnapshotAfterCheckout(t *testing.T) {
	e := newEngine(t, nil)
	p := mustProduct(t, e, "m1", "apple", 500, 10)
	e.SetItemQuantity("m1", "tok1", p.ID, 2)
	order, err := e.Checkout("m1", "tok1", "k1")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := e.UpdatePrice("m1", p.ID, 999); err != nil {
		t.Fatalf("update price: %v", err)
	}
	again, err := e.GetOrder("m1", order.ID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if again.Items[0].UnitPriceCents != 500 || again.TotalCents != 1000 {
		t.Fatalf("price changed on stored order: %+v", again)
	}
}
