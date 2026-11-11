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
| `unsupported_integration_test.go` | `notInThisVersion` | 骨架：还没做的路由答“能力不支持”（每做完一步就换掉它的路由，全做完时删掉这一步） | D75 | 入口 `payment/module.go` |

共用的：

| 文件 | 内容 |
| --- | --- |
| `endtoend_integration_test.go` | `TestPaymentEndToEnd`（各步的顺序）；`fixture`（运行中的成员、替身、世界的 ID：上海与北京两个法人、两个部门、一个客户、一个供应商、一个暂停的伙伴；Alice 与 Bob 两个用户）；`start`；辅助：`client.do`、`at`、`get`、`unit`、`publishOrgUnit`、`publishOrg`、`portFree` |
| `standins_integration_test.go` | 替身，各按已发布的契约经 mTLS 答：`mdm/org`（法人与部门）、`mdm/partner`（伙伴与它的状态）、`mdm/currency`、`erp/finance` 的 `ClaimService`、`erp/sales` 的 `OrderService.Read`（后两个随用到它们的步骤实现） |

## 加测试

- 新流程：新文件 `<流程>_integration_test.go`，写一个 `func (fx *fixture) <步>()`，在 `TestPaymentEndToEnd` 里按依赖排进顺序，在上表加一行。
- 测试不为了通过而改（装配项目规范的铁律）。
