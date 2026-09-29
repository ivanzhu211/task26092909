package checkout

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 压测：500 个并发结账抢 3 件库存、共用 5 次折扣码，
// 中间夹杂补货、改价、取消订单。跑完逐项对账。
func TestConcurrentCheckoutStorm(t *testing.T) {
	e := newEngine(t, nil)
	const merchant = "m1"
	const initialStock = 3
	const discountLimit = 5
	p := mustProduct(t, e, merchant, "hot", 1000, initialStock)
	if _, err := e.CreateDiscountCode(merchant, "FLASH", DiscountKindPercent, 10, 0, 0, discountLimit); err != nil {
		t.Fatalf("create code: %v", err)
	}

	const n = 500
	type result struct {
		order *Order
		err   error
		cost  time.Duration
	}
	results := make([]result, n)

	var restocked atomic.Int64
	cancelled := sync.Map{}
	orderIDs := make(chan int64, n)
	stop := make(chan struct{})
	var bg sync.WaitGroup

	// 后台：商户补货。
	bg.Add(1)
	go func() {
		defer bg.Done()
		for i := 0; i < 20; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := e.AdjustStock(merchant, p.ID, 1); err == nil {
				restocked.Add(1)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	// 后台：商户改价。
	bg.Add(1)
	go func() {
		defer bg.Done()
		prices := []int64{800, 1000, 1200, 1000, 900}
		for i := 0; i < 25; i++ {
			select {
			case <-stop:
				return
			default:
			}
			e.UpdatePrice(merchant, p.ID, prices[i%len(prices)])
			time.Sleep(time.Millisecond)
		}
	}()
	// 后台：取消已生成的订单。
	bg.Add(1)
	go func() {
		defer bg.Done()
		for id := range orderIDs {
			if _, err := e.CancelOrder(merchant, id); err == nil {
				cancelled.Store(id, true)
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := fmt.Sprintf("buyer-%d", i)
			if _, err := e.SetItemQuantity(merchant, token, p.ID, 1); err != nil {
				results[i] = result{err: err}
				return
			}
			if _, err := e.ApplyDiscount(merchant, token, "FLASH"); err != nil {
				results[i] = result{err: err}
				return
			}
			start := time.Now()
			order, err := e.Checkout(merchant, token, fmt.Sprintf("idem-%d", i))
			results[i] = result{order: order, err: err, cost: time.Since(start)}
			if err == nil && i%3 == 0 {
				orderIDs <- order.ID // 约三分之一的订单会被取消
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	close(orderIDs)
	bg.Wait()

	// 每个请求都必须在 10 秒内拿到业务结果，不能有技术性错误。
	var successes []*Order
	for i, r := range results {
		if r.err == nil {
			successes = append(successes, r.order)
			continue
		}
		if r.cost > 10*time.Second {
			t.Fatalf("request %d took %v", i, r.cost)
		}
		var stockErr *InsufficientStockError
		if errors.As(r.err, &stockErr) || errors.Is(r.err, ErrDiscountExhausted) {
			continue
		}
		t.Fatalf("request %d got unexpected error: %v", i, r.err)
	}
	t.Logf("successes=%d failures=%d cancelled=%d restocked=%d",
		len(successes), n-len(successes), countMap(&cancelled), restocked.Load())

	// 对账 1：每个成功的结账恰好对应一张订单（ID 唯一，且库里订单数一致）。
	seen := map[int64]bool{}
	for _, o := range successes {
		if seen[o.ID] {
			t.Fatalf("duplicate order id %d", o.ID)
		}
		seen[o.ID] = true
	}
	var orderCount int64
	if err := e.db.QueryRow(`SELECT COUNT(1) FROM orders WHERE merchant_id = ?`, merchant).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if orderCount != int64(len(successes)) {
		t.Fatalf("orders in db=%d, successes=%d", orderCount, len(successes))
	}

	// 对账 2：库存 = 初始 + 补货 - 未取消订单里的数量。
	var paidQty int64
	if err := e.db.QueryRow(
		`SELECT COALESCE(SUM(oi.quantity), 0) FROM order_items oi
		 JOIN orders o ON o.id = oi.order_id
		 WHERE o.merchant_id = ? AND o.status = ? AND oi.product_id = ?`,
		merchant, OrderStatusPaid, p.ID,
	).Scan(&paidQty); err != nil {
		t.Fatal(err)
	}
	got, err := e.GetProduct(merchant, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(initialStock) + restocked.Load() - paidQty
	if got.Stock != want {
		t.Fatalf("stock=%d, want %d (initial %d + restocked %d - paid %d)",
			got.Stock, want, initialStock, restocked.Load(), paidQty)
	}
	if got.Stock < 0 {
		t.Fatalf("oversold: stock=%d", got.Stock)
	}

	// 对账 3：折扣码未取消使用次数不超过上限。
	var discountUsed int64
	if err := e.db.QueryRow(
		`SELECT COUNT(1) FROM orders WHERE merchant_id = ? AND discount_code_id != 0 AND status = ?`,
		merchant, OrderStatusPaid,
	).Scan(&discountUsed); err != nil {
		t.Fatal(err)
	}
	if discountUsed > discountLimit {
		t.Fatalf("discount used %d times, limit %d", discountUsed, discountLimit)
	}

	// 对账 4：每张订单的总价能用明细重新算出来。
	for _, o := range successes {
		stored, err := e.GetOrder(merchant, o.ID)
		if err != nil {
			t.Fatal(err)
		}
		var subtotal int64
		for _, it := range stored.Items {
			subtotal += it.UnitPriceCents * it.Quantity
		}
		if subtotal != stored.SubtotalCents {
			t.Fatalf("order %d subtotal %d, recomputed %d", stored.ID, stored.SubtotalCents, subtotal)
		}
		if stored.SubtotalCents-stored.DiscountCents != stored.TotalCents {
			t.Fatalf("order %d total %d != subtotal %d - discount %d",
				stored.ID, stored.TotalCents, stored.SubtotalCents, stored.DiscountCents)
		}
		if stored.TotalCents < 0 {
			t.Fatalf("order %d negative total", stored.ID)
		}
	}
}

func countMap(m *sync.Map) int {
	n := 0
	m.Range(func(_, _ any) bool { n++; return true })
	return n
}
