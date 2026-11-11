# receipts：收款单

线下到账的登记：登记即到账，同一事务里发布资金事实给 `erp/finance` 核销；登记错了或银行退票用冲正（RC01、RC02）。**收款单的生命周期**只在这里。

## 负责与不负责

- 负责：表 `receipt`；路由 `/api/payment/receipts`（列表、登记、读取）与 `reverse`；资金事实 `receipt` 与它的冲正、事件 `erp.receipt.changed.v1`（完整状态）。
- 不负责：账户能不能用（`bankaccounts.Usable`）；事实的形状与发布（`facts.Fund`）；核销（`erp/finance`）。

## 生命周期（改收款规则先读这里）

`lifecycle.go` 是一张表（`core.Table`）：3 个状态（`none`、`received`、`reversed`）× 2 个命令（登记、冲正），规则 RC01、RC02。登记不经表（新建，同一幂等键答原来的收款单）；冲正在事务里锁住收款单、按格行事。状态的写入只有两处：`RegisterReceipt` 的插入、`ReverseReceipt` 的更新。

- 加一个状态或命令：表与 `docs/design.md` 的同一张表一起改（`lifecycle_test.go` 逐格比较），集成测试写上规则编号。

## 文件

| 文件 | 内容 |
| --- | --- |
| `lifecycle.go` | 状态、命令、附加条件；表 `Lifecycle` |
| `lifecycle_test.go` | 单元测试：每一格都定了；设计文档的表与代码逐格相同；每条规则都有集成测试写明 |
| `receipts.go` | `Receipt`；`Get`；`gather`（事务外取证：法人、账户、付款方、订单参考、币种小数位、汇率快照）；`RegisterReceipt`（内部号 `RC` 在事务之前取，允许缺号；同一账户的银行流水号在未冲正的收款里不重复 `BANK_REFERENCE_TAKEN`）、`GetReceipt`、`ReverseReceipt`（冲正事实照原事实，种类 `reversal`、指向原事实，业务日期是法人当地的今天）、`ListReceipts`（最新的在前，按单号与流水号搜索）；`changed`（完整状态事件与审计） |

## 依赖与被依赖

- 依赖：`bankaccounts`、`core`、`facts`、`masterdata`。
- 被依赖：入口登记路由（以后：对账匹配）。

## 规则在哪

`docs/design.md` 的“收款单”（表与 RC01、RC02）与“规则要点”（外币、规则键）；资金事实：`erp/finance` 的共享契约与装配项目协议 05。

## 测试

- 单元：`lifecycle_test.go`。
- 集成：`itest/receipts_integration_test.go`（`receipts`：RC01、RC02、资金事实合乎财务的共享 schema、`FactService.Read`）。
