# bankaccounts：公司银行账户

法人在银行开的账户：收款、付款、调拨与对账单行都记在一个账户上。账号是受限字段（安全 09a，读取键 `core.NumberKey`）：授予里列出它的人看全，其他人只看到后四位。

## 负责与不负责

- 负责：表 `bank_account`；路由 `/api/payment/bank-accounts`（列表、新建、读取、修改名称与状态）；单据能不能记在一个账户上（`Usable`：存在、属于单据的法人、启用；`FOR SHARE` 锁到事务结束）。
- 不负责：账户上的单据（各自的模块）；账户的期初余额与未达账项（不在首批）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `accounts.go` | `Account`（`Mask`：按可读字段遮蔽账号）；`Get`、`Usable`；`CreateBankAccount`（法人与币种在用，同一法人账号不重复 `CODE_TAKEN`）、`GetBankAccount`、`UpdateBankAccount`（按版本）、`ListBankAccounts`（按名称；每行连同 SDK 的 `q.Hits` 一起选出，按这一行可读的字段遮蔽） |

## 依赖与被依赖

- 依赖：`core`、`masterdata`。
- 被依赖：`receipts`（以后：付款申请、调拨与收付、对账）；入口登记路由。

## 规则在哪

`docs/design.md` 的“规则要点”（账号是受限字段）与“拥有的数据”；受限字段：装配项目安全 07、安全 09a。

## 测试

集成：`itest/bankaccounts_integration_test.go`（`bankAccounts`）。
