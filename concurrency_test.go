package checkout

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// isBusinessError 失败原因必须是业务错误之一，不能是技术性错误。
func isBusinessError(err error) bool {
	var shortage *InsufficientStockError
	switch {
	case errors.As(err, &shortage),
		errors.Is(err, ErrProductOffShelf),
		errors.Is(err, ErrCartExpired),
		errors.Is(err, ErrBelowMinSpend),
		errors.Is(err, ErrDiscountExhausted),
		errors.Is(err, ErrIdempotencyConflict),
		errors.Is(err, ErrCartFrozen),
		errors.Is(err, ErrValidation),
		errors.Is(err, ErrNotFound):
		return true
	}
	return false
}

// TestConcurrentCheckoutStorm 模拟促销洪峰：
// 500 个并发结账抢 3 件爆款，共用一个只剩 5 次的折扣码，
// 中间夹着补货、改价和取消订单。最后逐项对账。
func TestConcurrentCheckoutStorm(t *testing.T) {
	e := openTest(t)
	const merchant = "m1"
	const requests = 500

	hot, err := e.CreateProduct(merchant, "hot", 1000, 3)
	if err != nil {
		t.Fatal(err)
	}
	normal, err := e.CreateProduct(merchant, "normal", 500, 400)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateDiscountCode(DiscountCode{
		MerchantID: merchant, Code: "FLASH", Kind: DiscountPercent, Percent: 20, MaxUses: 5,
	}); err != nil {
		t.Fatal(err)
	}

	// 每个请求一辆车：一半抢爆款（爆款+普通），一半只买普通商品；一半挂折扣码。
	for i := 0; i < requests; i++ {
		token := fmt.Sprintf("tok-%d", i)
		if i%2 == 0 {
			if err := e.SetItemQuantity(merchant, token, hot, 1); err != nil {
				t.Fatalf("add hot: %v", err)
			}
		}
		if err := e.SetItemQuantity(merchant, token, normal, 1); err != nil {
			t.Fatalf("add normal: %v", err)
		}
		if i%3 == 0 {
			if err := e.ApplyDiscount(merchant, token, "FLASH"); err != nil {
				t.Fatalf("apply discount: %v", err)
			}
		}
	}

	type result struct {
		order   *Order
		err     error
		elapsed time.Duration
	}
	results := make([]result, requests)

	// 记录测试期间成功的库存调整与取消，用于对账。
	var mu sync.Mutex
	var stockAdjusts int64
	var cancelledOrders []int64
	orderCh := make(chan int64, requests)
	stopCh := make(chan struct{})
	var bgWg sync.WaitGroup

	// 后台：补货。
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				if err := e.AdjustStock(merchant, normal, 1); err == nil {
					mu.Lock()
					stockAdjusts++
					mu.Unlock()
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()
	// 后台：改价（不应影响已生成订单，也不应造成技术性错误）。
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		price := int64(500)
		for {
			select {
			case <-stopCh:
				return
			default:
				price += 1
				_ = e.SetPrice(merchant, normal, price)
				time.Sleep(time.Millisecond)
			}
		}
	}()
	// 后台：取消已成功的订单（取消约 1/4）。
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		i := 0
		for id := range orderCh {
			if i%4 == 0 {
				if err := e.CancelOrder(merchant, id); err == nil {
					mu.Lock()
					cancelledOrders = append(cancelledOrders, id)
					mu.Unlock()
				}
			}
			i++
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start := time.Now()
			order, err := e.Checkout(merchant, fmt.Sprintf("tok-%d", i), fmt.Sprintf("key-%d", i))
			results[i] = result{order: order, err: err, elapsed: time.Since(start)}
			if err == nil {
				orderCh <- order.ID
			}
		}(i)
	}
	wg.Wait()
	close(stopCh)
	close(orderCh)
	bgWg.Wait()

	// ---- 校验每个请求的结果 ----
	var successes []int64
	var maxElapsed time.Duration
	for i, r := range results {
		if r.elapsed > 10*time.Second {
			t.Fatalf("request %d took %v (>10s)", i, r.elapsed)
		}
		if r.elapsed > maxElapsed {
			maxElapsed = r.elapsed
		}
		if r.err == nil {
			successes = append(successes, r.order.ID)
			continue
		}
		if !isBusinessError(r.err) {
			t.Fatalf("request %d got technical error: %v", i, r.err)
		}
	}
	t.Logf("successes=%d failures=%d maxLatency=%v cancels=%d restocks=%d",
		len(successes), requests-len(successes), maxElapsed, len(cancelledOrders), stockAdjusts)

	// ---- 对账 1：爆款不超卖 ----
	hotP, _ := e.GetProduct(merchant, hot)
	if hotP.Stock < 0 {
		t.Fatalf("oversold: hot stock = %d", hotP.Stock)
	}

	// ---- 对账 2：普通商品库存 = 初始 + 成功调整 - 未取消订单中的数量 ----
	cancelledSet := map[int64]bool{}
	for _, id := range cancelledOrders {
		cancelledSet[id] = true
	}
	var normalSold, hotSoldActive int64
	discountActiveUses := 0
	for _, id := range successes {
		o, err := e.GetOrder(merchant, id)
		if err != nil {
			t.Fatalf("get order %d: %v", id, err)
		}
		// 对账 5：总价可用明细重算。
		var subtotal int64
		for _, l := range o.Lines {
			if l.LineTotalCents != l.UnitPriceCents*l.Quantity {
				t.Fatalf("order %d line total mismatch: %+v", id, l)
			}
			subtotal += l.LineTotalCents
		}
		if subtotal != o.SubtotalCents || o.TotalCents != o.SubtotalCents-o.DiscountCents {
			t.Fatalf("order %d totals mismatch: %+v", id, o)
		}
		if o.TotalCents < 0 {
			t.Fatalf("order %d negative total", id)
		}
		if cancelledSet[id] {
			if !o.Cancelled {
				t.Fatalf("order %d should be cancelled", id)
			}
			continue
		}
		for _, l := range o.Lines {
			switch l.ProductID {
			case normal:
				normalSold += l.Quantity
			case hot:
				hotSoldActive += l.Quantity
			}
		}
		if o.DiscountCode == "FLASH" {
			discountActiveUses++
		}
	}
	normalP, _ := e.GetProduct(merchant, normal)
	wantNormal := int64(400) + stockAdjusts - normalSold
	if normalP.Stock != wantNormal {
		t.Fatalf("normal stock = %d, want %d (400 + %d adjusts - %d sold)",
			normalP.Stock, wantNormal, stockAdjusts, normalSold)
	}
	wantHot := int64(3) - hotSoldActive
	if hotP.Stock != wantHot {
		t.Fatalf("hot stock = %d, want %d", hotP.Stock, wantHot)
	}

	// ---- 对账 3：折扣码未取消使用次数 <= 上限 ----
	if discountActiveUses > 5 {
		t.Fatalf("discount active uses = %d > 5", discountActiveUses)
	}

	// ---- 对账 4：每个成功的结账恰好对应一张订单（幂等重放返回同一张）----
	seen := map[int64]bool{}
	for i, r := range results {
		if r.err != nil {
			continue
		}
		if seen[r.order.ID] {
			t.Fatalf("duplicate order id %d", r.order.ID)
		}
		seen[r.order.ID] = true
		replay, err := e.Checkout(merchant, fmt.Sprintf("tok-%d", i), fmt.Sprintf("key-%d", i))
		if err != nil {
			t.Fatalf("idempotent replay %d: %v", i, err)
		}
		if replay.ID != r.order.ID {
			t.Fatalf("replay got order %d, want %d", replay.ID, r.order.ID)
		}
	}
}

// TestConcurrentDuplicateIdempotencyKey 同一幂等键被并发重复提交。
func TestConcurrentDuplicateIdempotencyKey(t *testing.T) {
	e := openTest(t)
	pid := mustProduct(t, e, "m1", "a", 100, 50)
	e.SetItemQuantity("m1", "tok", pid, 2)

	const n = 50
	ids := make([]int64, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, err := e.Checkout("m1", "tok", "same-key")
			if err == nil {
				ids[i] = o.ID
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("duplicate submit %d failed: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("duplicate submit got different order: %d vs %d", ids[i], ids[0])
		}
	}
	// 只生成了一张订单，只扣了一次库存。
	p, _ := e.GetProduct("m1", pid)
	if p.Stock != 48 {
		t.Fatalf("stock = %d, want 48 (single deduction)", p.Stock)
	}

	// 同键并发打到两辆不同的车：恰好一个成功，其余报幂等冲突。
	e.SetItemQuantity("m1", "cart-a", pid, 1)
	e.SetItemQuantity("m1", "cart-b", pid, 1)
	// 赢的那辆车：首次成功 + 后续同键同车重放都返回同一张订单；
	// 另一辆车：全部报幂等冲突。全局只生成一张订单。
	var okIDs []int64
	var conflictCount int
	var cmu sync.Mutex
	var wg2 sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg2.Add(1)
		go func(i int) {
			defer wg2.Done()
			token := "cart-a"
			if i%2 == 0 {
				token = "cart-b"
			}
			o, err := e.Checkout("m1", token, "shared-key")
			cmu.Lock()
			defer cmu.Unlock()
			if err == nil {
				okIDs = append(okIDs, o.ID)
			} else if errors.Is(err, ErrIdempotencyConflict) {
				conflictCount++
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg2.Wait()
	if len(okIDs) == 0 {
		t.Fatal("no success at all")
	}
	for _, id := range okIDs {
		if id != okIDs[0] {
			t.Fatalf("different orders created: %d vs %d", id, okIDs[0])
		}
	}
	if conflictCount != 20-len(okIDs) {
		t.Fatalf("conflictCount = %d, want %d", conflictCount, 20-len(okIDs))
	}
}
