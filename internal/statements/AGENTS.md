# statements：对账单行与对账

银行对账单的行：按账户成批登记、自动或手工与单据匹配、差错的处理与重新打开、从一行生成手续费或利息、一个账户的对账情况（BR01–BR04）。**对账单行的生命周期**只在这里；匹配本身在 `matching`。

## 负责与不负责

- 负责：表 `statement_line`；路由 `/api/payment/statement-lines`（登记、列表）与 `match`、`unmatch`、`resolve`、`reopen`（`Action`）、`create-entry`（`CreateEntryFromLine`）、`/api/payment/bank-accounts/:id/reconciliation`（`Reconciliation`）。
- 不负责：哪些单据能匹配、怎样匹配（`matching`）；生成的单据的审批与过账（`entries`）。

## 生命周期（改对账规则先读这里）

`lifecycle.go` 是一张表（`core.Table`）：3 个状态（`unmatched`、`matched`、`resolved`）× 5 个命令，附加条件 `GuardMatchable`、`GuardNote`、`GuardNoDraft`，规则 BR01–BR04。匹配的写入在 `matching.Match`；取消匹配、处理差错、重新打开的写入在 `Action`。

- 加一个状态或命令：表与 `docs/design.md` 的同一张表一起改（`lifecycle_test.go` 逐格比较），集成测试写上规则编号。

## 文件

| 文件 | 内容 |
| --- | --- |
| `lifecycle.go` | 状态、命令、附加条件；表 `Lifecycle` |
| `lifecycle_test.go` | 单元测试：每一格都定了；设计文档的表与代码逐格相同；每条规则都有集成测试写明 |
| `statements.go` | `Line`、`Document`；`account`（按账户的法人授权）、`signed`；`RegisterLines`（幂等；带流水号的按“账户 + 流水号”去重，跳过的列在答复里；每行登记后按流水号匹配一次）、`ListLines`（按日期）、`Action`、`CreateEntryFromLine`（付的行生成手续费、收的行生成利息的草稿，一行只生成一张）、`Reconciliation`（行的合计、已匹配与未匹配的合计、这边没被匹配的单据）、`reduce` |

## 依赖与被依赖

- 依赖：`bankaccounts`、`core`、`entries`、`masterdata`、`matching`。
- 被依赖：入口登记路由。

## 规则在哪

`docs/design.md` 的“对账单行”（表与 BR01–BR04）；从对账单行生成科目收付：装配项目协议 05 的 BL84。

## 测试

- 单元：`lifecycle_test.go`。
- 集成：`itest/statements_integration_test.go`（`statements`：BR01–BR04）。
