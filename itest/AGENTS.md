# itest/：集成测试与测试索引

收付款组件对着真实的 PostgreSQL 与消息系统、经它真实的接口（用户面 HTTP、系统面 gRPC、事件）的测试（L2）。构建标签 `integration`；一条业务流程一个文件。

## 怎样跑

- `make test-integration`（`go test -count=1 -tags integration ./itest/`）。
- 环境变量：`BE_SDK_TEST_PG_ADMIN`（口令是装配项目 `.env` 的 `POSTGRES_PASSWORD`）、`BE_SDK_TEST_NATS_URL=nats://localhost:4222`；be-sdk-go 的 `sectest` 是测试部署。
- 前提：装配项目里 `make up` 起了 `be-postgres` 与 `be-nats`。
- 端口：28228（用户面）、9228（系统面）、替身 19130（`mdm/org`）、19131（`mdm/partner`）、19132（`mdm/currency`）、19133（`erp/finance`）、19134（`erp/sales`）要空着，被占时测试直接失败。
- 每次开始：删掉再建自己的 schema `erp_payment_it`（跑完留着，可以查）；删掉流 `mdm.org`、`erp.payment`（别的运行留下的事件）。
- **只有一个测试** `TestPaymentEndToEnd`：它按下表的顺序跑各步；改了哪个流程就跑整个。

## 文件

按运行顺序：

| 文件 | 步 | 测的流程 | 覆盖的规则 | 被测的模块 |
| --- | --- | --- | --- | --- |
| `bankaccounts_integration_test.go` | `bankAccounts` | 上海的人民币、美元、停用的账户与北京的账户；新建的拒绝（法人、币种、同法人账号重复、格式）；别的法人可以同号；Carol 的授予不列受限字段，单条与列表都只看到后四位；按版本改名、停用 | 安全 09a | `bankaccounts` |
| `receipts_integration_test.go` | `receipts` | 登记即到账、资金事实合乎财务的共享 schema、完整状态事件；同一幂等键答同一张；登记的拒绝（流水号重复、停用或别的法人的账户、暂停的付款方、不是付款方的订单、金额为零）；订单参考随事实、付款方不明为空、美元账户带汇率快照；冲正写原因、冲正事实指向原事实、重复的忽略、冲正后流水号可再用；列表；`FactService.Read` | RC01、RC02 | `receipts`、`bankaccounts`、`facts` |
| `requests_integration_test.go` | `requests` | 财务的替身放入应付、客户贷方余额、美元应付与应收（P1 上另有 300 的认领）：付款建议；新建的拒绝（没有未清项、同一项两次、停用账户、暂停的收款方、别的法人的部门）；提交时 P1 不够整张不提交、已认领的 P2 释放；改了再提交；申请人不能批准、不能放行；放行确认认领；执行 750（按行的顺序分到 P1 700、P2 50），资金事实带每条认领的实付，再执行忽略、只有一个事实；退回发冲正事实；退款被驳回释放认领、再提交换新认领；放行后取消释放已确认的认领；收款方、种类、币种不符的未清项与应收在认领之前被拒；美元账户付美元应付、逐行给实付、带汇率快照；放行时认领已失效答 `CLAIM_LOST`；认领期限 4 秒时续期保住等着的申请、续期被拒的退回草稿并释放其余认领；列表；Carol 看不到 | PR01–PR08 | `requests`、`facts` |
| `entries_integration_test.go` | `entries` | 新建的拒绝（跨币种、转给自己、转给停用或别的法人的账户、手续费写了转入账户、停用账户）；手续费按版本改、提交后不能改、制单人不能批准、另一人批准过账发 `bank_charge`；过账后不能取消；美元利息被驳回写原因、再提交、过账带汇率快照；调拨批准进在途发 `fund_transfer_out`、在途不能取消、到账日期早于转出被拒、到账发 `fund_transfer_in`（两条同一来源，再到账忽略）；草稿取消；按种类列表 | BE01–BE04 | `entries`、`facts` |
| `statements_integration_test.go` | `statements` | CNY 账户登记 7 行：R-1（已到账的那张，冲正过的不算）、R-3 按流水号自动匹配，退回的付款 P-1 不匹配；同一流水号再登记被跳过、没有流水号的照登；之后登记的 X-9 收款找到它的行；手工匹配手续费、取消再匹配，金额不同、已被别的行匹配、在别的账户上的被拒；调拨在两个账户各匹配一条腿；从短信费的行生成手续费草稿、不能再生成、批准后匹配回来源行；差错写处理说明、重新打开；对账情况的合计与没被匹配的单据 | BR01–BR04 | `statements`、`matching`、`entries` |

共用的：

| 文件 | 内容 |
| --- | --- |
| `endtoend_integration_test.go` | `TestPaymentEndToEnd`（各步的顺序）；`fixture`（运行中的成员、替身、世界的 ID：上海与北京两个法人、两个部门、一个客户、一个供应商、一个暂停的伙伴；Alice 与 Bob 两个用户，Carol 只读且不读受限字段）；`start`；辅助：`client.do`、`at`、`get`、`unit`、`publishOrgUnit`、`publishOrg`、`portFree` |
| `standins_integration_test.go` | 替身，各按已发布的契约经 mTLS 答：`mdm/org`（法人与部门）、`mdm/partner`（伙伴与它的状态）、`mdm/currency`（含 `RateService`：USD→CNY 7.1）、`erp/sales` 的 `OrderService.Read`（测试按身份放入订单：`salesStandIn.set`）、`erp/finance` 的 `ClaimService`（付款的认领：测试放入未清项 `item`、别人的认领 `claimFor`，让认领失效 `lose`，可改期限 `period`；拒绝时带财务的原因） |

## 加测试

- 新流程：新文件 `<流程>_integration_test.go`，写一个 `func (fx *fixture) <步>()`，在 `TestPaymentEndToEnd` 里按依赖排进顺序，在上表加一行。
- 测试不为了通过而改（装配项目规范的铁律）。
