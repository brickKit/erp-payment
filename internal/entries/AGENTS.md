# entries：资金调拨与科目收付

不针对伙伴未清项的资金：公司账户之间的调拨（转出进在途资金，到账再出来）、银行手续费与利息；经审批后发布会计事实（协议 05，BE01–BE04）。**调拨与收付的生命周期**只在这里。

## 负责与不负责

- 负责：表 `bank_entry`；路由 `/api/payment/entries`（列表、新建、读取、修改）与 `submit`、`approve`、`reject`、`arrive`、`cancel`（`Action`）；会计事实 `fund_transfer_out`、`fund_transfer_in`、`bank_charge`、`bank_interest`（来源单据都是这张单，调拨的两条同一来源）。
- 不负责：科目（财务按法人的映射）；账户能不能用（`bankaccounts.Usable`）；事实的形状（`facts.Accounting`）。

## 生命周期（改调拨与收付的规则先读这里）

`lifecycle.go` 是一张表（`core.Table`）：5 个状态 × 6 个命令，附加条件 `GuardNotMaker`（批准人不是制单人；批准时手续费与利息直接到 `posted`，表里那一格写的是调拨的 `in_transit`）、`GuardReason`、`GuardArrival`，规则 BE01–BE04。状态的写入只在 `actions.go` 的 `apply`；修改只在 `UpdateEntry`（草稿）。

- 加一个状态或命令：表与 `docs/design.md` 的同一张表一起改（`lifecycle_test.go` 逐格比较），集成测试写上规则编号。

## 文件

| 文件 | 内容 |
| --- | --- |
| `lifecycle.go` | 状态、命令、附加条件；表 `Lifecycle` |
| `lifecycle_test.go` | 单元测试：每一格都定了；设计文档的表与代码逐格相同；每条规则都有集成测试写明 |
| `entries.go` | `Entry`；`Get`；`EntryIn`、`check`（事务外取证：法人、账户都在用、调拨同币种且是两个账户、只有调拨写转入账户、金额小数位）与给对账用的 `Check`；`Create`（内部号 `BE` 在事务之前取；可记来源的对账单行）、`CreateEntry`、`UpdateEntry`、`GetEntry`、`ListEntries`；`fact`（一张单的会计事实） |
| `actions.go` | `Action`（汇率快照在事务前取：批准按单据日期，到账按到账日期）、`apply`（每个命令的写入与事实） |

## 依赖与被依赖

- 依赖：`bankaccounts`、`core`、`facts`、`masterdata`。
- 被依赖：入口登记路由（以后：对账单行生成科目收付）。

## 规则在哪

`docs/design.md` 的“资金调拨与科目收付”（表与 BE01–BE04）；调拨成对、在途：装配项目协议 05 与 `erp/finance` 的规则目录（3.0.2 起的 `fund_transfer_out`、`fund_transfer_in`）。

## 测试

- 单元：`lifecycle_test.go`。
- 集成：`itest/entries_integration_test.go`（`entries`：BE01–BE04）。
