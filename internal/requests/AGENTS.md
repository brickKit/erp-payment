# requests：付款建议与付款申请

要付给一个伙伴的钱：针对 `erp/finance` 的未清项（付款对应付，退款对客户贷方余额），提交时认领、批准、放行时确认认领、人去银行付了之后登记执行，发布带认领清单的资金事实；没付成就退回（PR01–PR08）。**付款申请的生命周期**只在这里。

## 负责与不负责

- 负责：表 `payment_request`（带 `department_path`，在 `core.DepartmentTables` 里）、`payment_request_line`；路由 `/api/payment/proposals`（财务的到期未清项）、`/api/payment/requests`（列表、新建、读取、修改）与 `submit`、`approve`、`reject`、`release`、`execute`、`return`、`cancel` 七个动作（`Action`）；后台工作 `renew-claim`（`RenewClaim`）；资金事实 `payment`、`refund` 与它们的冲正、事件 `erp.payment_request.changed.v1`。
- 不负责：认领的额度与正式化（`erp/finance`）；账户能不能用（`bankaccounts.Usable`）；部门（`orgunits.Usable`）；事实的形状（`facts.Fund`）。

## 生命周期（改付款规则先读这里）

`lifecycle.go` 是一张表（`core.Table`）：7 个状态 × 9 个命令（含后台的 `lapse`），附加条件 `GuardClaimed`、`GuardNotApplicant`、`GuardConfirmed`、`GuardReason`（在 `actions.go` 里检查），规则 PR01–PR08。

- 用法：命令先在事务外调财务（核对收款方的未清项、认领、确认、释放，都按身份幂等），再由 `moved` 在事务里锁住申请、再问一次表、写下变化（并发时以现在的状态为准）。状态的写入都经 `moved`；修改只在 `UpdateRequest`（草稿）。
- 提交被拒时已认领的要释放；提交时如果申请已被另一个调用提交，这次的认领也释放。
- 加一个状态或命令：表与 `docs/design.md` 的同一张表一起改（`lifecycle_test.go` 逐格比较），集成测试写上规则编号。

## 文件

| 文件 | 内容 |
| --- | --- |
| `lifecycle.go` | 状态、命令、附加条件；表 `Lifecycle` |
| `lifecycle_test.go` | 单元测试：每一格都定了；设计文档的表与代码逐格相同；每条规则都有集成测试写明 |
| `records.go` | `Request`、`Line`；读取的 SQL（`cols`、`scan`、`lines`、`Get`、`Read`）；`facts`（两个维度）、`claims`、`changed`（完整状态事件与审计；后台的失效不审计） |
| `requests.go` | `prepare`（事务外取证：法人、账户与币种、收款方、部门、行金额；装了财务时每行必须有未清项，没装时不能有）、`writeLines`；`CreateRequest`（内部号 `PR` 在事务之前取）、`UpdateRequest`（草稿，按版本）、`GetRequest`、`ListRequests`、`ListProposals`（转财务的 `DueItems`） |
| `actions.go` | `Action`、`moved`；`payable`（PR02：按收款方、种类、币种在财务的到期未清项里核对）、`releaseAll`；`submit`、`approve`、`giveBack`（驳回与取消）、`release`、`split`（逐行或按顺序分实付）、`execute`、`returned` |
| `checks.go` | `CheckHeldClaims`、`HeldClaims`（整套恢复的检查：已放行的申请在财务那边的认领不是已确认的报出来，事务之后才问财务；不改状态） |
| `renewal.go` | `JobRenewClaim`、`RenewClaim`（PR08：续期并排下一次；被拒则 `lapse` 回草稿、释放其余认领）、`renewAt`（期限前一小时，期限更短时在中点）、`scheduleRenewal`、`holds` |

## 依赖与被依赖

- 依赖：`bankaccounts`、`core`、`facts`、`masterdata`、`matching`、`orgunits`。
- 被依赖：入口登记路由、工作与恢复检查。

## 规则在哪

`docs/design.md` 的“付款申请”（表与 PR01–PR08）与“规则要点”；付款认领：装配项目协议 05；职责分离：协议 09。

## 测试

- 单元：`lifecycle_test.go`。
- 集成：`itest/requests_integration_test.go`（`requests`：PR01–PR08，财务的认领服务用替身）；`itest/recovery_integration_test.go`（检查 `erp.payment.held_claims`）。没装财务的路径（行不带未清项、不认领）首批没有集成测试：测试部署总装着财务的替身。
