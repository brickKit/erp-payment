# facts：按身份读回增量事实

本组件发布的两种增量事实：资金事实与会计事实的形状（`erp/finance` 的共享契约 `fund_fact.v1`、`accounting_fact.v1`）与发布（经 `core.Fact`，与单据同一个事务）；系统面 `FactService.Read`：任一组件按事实身份读回它们，漏收的消费方据此补读（协议 05 的共同规则）。

## 负责与不负责

- 负责：`Fund`、`Accounting` 与它们的 `Publish`；读 `payment_fact`，按问的顺序回答，不认识的身份类型为空。
- 不负责：什么时候发、发什么金额（各单据的模块）；契约的形状本身（财务的共享契约，改要先改那边）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `facts.go` | `Read` |
| `shapes.go` | `Source`、`Dimensions`、`ClaimAmount`、`OrderRef`、`Fund`、`Accounting`（字段照共享契约）、`Now`、两个 `Publish` |

## 依赖与被依赖

- 依赖：`core`、`masterdata`（汇率快照的形状）。
- 被依赖：`receipts`（以后：付款申请、调拨与收付）；入口的 `services.go`。

## 规则在哪

装配项目协议 05 的“共同规则”（不可遗漏的事实、按身份补读）。

## 测试

集成：`itest/receipts_integration_test.go`（事实合乎财务的共享 schema、`FactService.Read` 读回）。
