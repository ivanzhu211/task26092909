# checkout

嵌入式购物车挂件的结账引擎（Go 库 + SQLite），由店铺挂件后端直接调用，无接口层。

## 用法

```go
e, _ := checkout.Open("shop.db")          // 或 ":memory:"
defer e.Close()

// 商户侧
p, _ := e.CreateProduct("merchant-1", "apple", 500, 10) // 价格单位：分
e.AdjustStock("merchant-1", p.ID, 5)
e.UpdatePrice("merchant-1", p.ID, 450)
e.SetProductStatus("merchant-1", p.ID, checkout.ProductStatusOffShelf)
e.CreateDiscountCode("merchant-1", "SAVE10", checkout.DiscountKindPercent, 10, 0, 1000, 100)

// 买家侧（凭会话令牌，无需登录）
e.SetItemQuantity("merchant-1", "session-token", p.ID, 2) // 0 = 移除
e.ApplyDiscount("merchant-1", "session-token", "SAVE10")
order, err := e.Checkout("merchant-1", "session-token", "idempotency-key")
e.CancelOrder("merchant-1", order.ID)
```

## 语义要点

- 结账在单个事务里完成校验、扣库存、占用折扣次数：要么全成功，要么零改动。
- 幂等键只在商户内唯一；同键同车重放返回同一张订单，同键不同车报错；
  失败的结账不占键，可原样重试。
- 购物车最后一次修改满 72 小时即过期（含恰满 72 小时这一刻）。
- 取消订单归还库存并释放折扣次数；重复取消幂等，不会重复归还。
- 引擎内部使用单连接串行化所有事务，并发下不会返回技术性冲突错误。

## 测试

```sh
go test ./...
```

覆盖：500 并发结账抢库存/折扣（夹杂补货、改价、取消）+ 五项对账、
并发重复幂等键、72 小时过期边界、商户隔离、价格快照等。
