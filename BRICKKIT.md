# erp/payment

## 组件定位

收付款：钱的结果，首批只有线下登记（D47）。
- **公司银行账户**：按法人维护，收付款都记在一个账户上；账号是受限字段。
- **收款**：登记到账（付款方不知道时财务记暂挂），登记即发布资金事实给 `erp/finance` 核销；登记错了冲正。
- **付款**：付款建议列出财务的到期未清项；付款申请提交时向财务认领、审批、放行时确认认领、人去银行付了之后登记执行（可以部分付），发布带认领清单的资金事实，财务按实付核销；没付成就退回。
- **资金调拨与科目收付**：账户之间的调拨（转出与到账两步，之间记在途资金）、银行手续费与利息，经审批后发布会计事实。
- **审批**：付款申请与调拨收付的审批走 SDK 的流程内核（线协议第 17 章）：法人的付款政策给出审批人（`mdm/org` 的一个岗位：法人当天在岗的人；或指定的一个主体），付款总要人审批；提交时启动审批实例、建任务，任务以共享任务契约发布给统一待办（`infra/workflow`）；批准或驳回是对任务的决定（驳回必须附意见），职责分离以人为单位（同一员工的两个账号算一个人），每个任务至多一个决定；放行与执行前做放行核对。
- **银行对账**：登记对账单行，与收付款自动或手工匹配，差错登记与处理，可以从对账单行生成手续费或利息。

总账、未清项与核销在 `erp/finance`；对银行或支付渠道发出指令、接收回调的适配器不在本版。本版没有：经适配器的对外支付、关账的封存、预收放行认领、预付与员工借款、银行账户的期初余额与未达账项。

**3.0.0**（第一个重建的版本，契约 `contract/v3.0.0-alpha.2`）：公司银行账户、收款登记与冲正、付款建议与付款申请（认领、审批、放行、执行、退回）、资金调拨与科目收付、银行对账、`FactService.Read`、整套恢复的核对。

## 部署前准备

- 数据库：PostgreSQL 16 及以上；schema `erp_payment`，属主角色 `erp_payment_owner`（只供迁移）与运行角色 `erp_payment_rw`（只有读写数据的权限），名字来自装配项目的注册表。三者由部署方在第一次启动前建好；表由本组件的迁移建，迁移里不建角色、不授权。
- 消息系统（`BUS_URL`）与实现实例（`IMPLEMENTATION_INSTANCE`）：资金事实与会计事实以增量事实，收款单与付款申请以完整状态发布到流 `erp.payment`（4 个分区）；订阅 `mdm/org` 的部门事件（`mdm.org_unit.>`）。装了 `erp/finance` 时，它的配置键 `FINANCE_FACT_SOURCES` 要列出 `erp.payment` 的 `erp.payment.fund_fact.v1` 与 `erp.payment.accounting_fact.v1`，否则财务收不到事实。
- 部署的服务证书（`SERVICE_TLS_*`）；身份与授权（`IAM_*`、`AUTHZ_GRPC_URL`、`DEPLOYMENT_ID`）；审计（`AUDIT_GRPC_URL`）。
- 审批：每个法人在付款政策里配审批人，没配时提交答 409 `NO_APPROVER_CONFIGURED`（付款申请什么也不认领）。按岗位审批要装 `mdm/employee`（本组件补取它的流 `mdm.employee` 并读快照，投影任职与账号关联）；**没装员工组件时**，政策只能用指定个人（配了岗位的任务暂停 `no_assignee`），职责分离（审批人、放行人不是申请人）只能比较账号：同一个人用第二个账号能批准或放行自己的付款申请（线协议 P17.12），装配时要告知客户或装员工组件。
- 任务暂停（岗位上没人、员工投影不完整）时记一条告警日志（`event=flow_no_assignee`），有 `erp.payment.flow.manage` 的人在 `GET /api/payment/flow/tasks?state=paused` 看到；员工投影变化后每分钟重新解析。
- 低到中负载：适合外壳。

## 依赖说明

- 必需：数据库、消息系统、身份成员、授权成员、`mdm/org`、`mdm/partner`、`mdm/currency`。
- 可选：`erp/finance`（付款认领与付款建议；缺时付款申请不针对未清项、不认领，只线下登记、人工核对，事实照样发布）；`erp/sales`（收款的订单参考；缺时收款不能带订单参考）；`mdm/employee`（审批人按岗位、职责分离以人为单位；缺时见部署前准备）；审计组件。
- 主数据或财务不可达时：要取证或认领的命令答“依赖不可用”、什么也不改，可以重试；已有数据照常读取。

## 配置指南

全部键见 `component.yaml` 的 `configSchema`（由 SDK 生成，测试核对）。

| 键 | 说明 |
| --- | --- |
| `DEPLOYMENT_ID`、`IAM_ISSUER`、`IAM_JWKS_URL`、`AUTHZ_GRPC_URL`、`SERVICE_TLS_CERT_FILE`、`SERVICE_TLS_KEY_FILE`、`SERVICE_TLS_CA_FILE`（必填） | 受保护的用户面与 mTLS 的系统面要用；部署身份、身份与授权的地址在外壳里是进程级的键 |
| `PG_HOST`、`PG_DATABASE`、`PG_USER`、`PG_PASSWORD`、`PG_SCHEMA`（必填） | 自己的数据库，密码是密钥；迁移另用 `PG_OWNER_USER`、`PG_OWNER_PASSWORD` |
| `BUS_URL`（必填，密钥）、`IMPLEMENTATION_INSTANCE`（必填） | 消息系统；实现实例是安装时生成的随机身份 |
| `AUDIT_GRPC_URL` | 审计组件的系统面；缺时审计记录留在本地，按协议 03 处理 |
| `FLOW_ACTION_SOURCES` | 审批任务接受哪些渠道成员的按钮动作：组件 ID，逗号分隔（例如 `integration/im-dingtalk`）；不填时卡片只有链接、没有动作按钮。列出的成员要同时装上，它的动作事件流由 SDK 订阅（线协议 P17.19、P17.20） |

依赖的地址由 BrickKit 按依赖注入（`MDM_ORG_GRPC_ENDPOINT`、`MDM_PARTNER_GRPC_ENDPOINT`、`MDM_CURRENCY_GRPC_ENDPOINT`，装了时还有 `ERP_FINANCE_GRPC_ENDPOINT`、`ERP_SALES_GRPC_ENDPOINT`、`MDM_EMPLOYEE_GRPC_ENDPOINT`），不是配置键。审批人是按法人的付款政策，经用户面维护（改审批人只影响之后提交的单据）。单号格式 `RC`、`PR`、`BE` 加 `-<法人代码>-<年>-<六位序号>`（允许缺号）。

## 契约索引

- **系统面**（本仓库 `contract/proto`，模块 `github.com/brickKit/erp-payment/contract/v3`，标签 `contract/v3.…`）：`be.erp.payment.v3.FactService/Read`（增量事实按身份读回；任一组件，操作 `erp.payment.system.read`）。
- **用户面**：`contract/openapi.yaml`。操作 `erp.payment.bank_account.read`、`.manage`（全账号要授予里列出受限字段，读取键 `erp.payment.bank_account.number`），`erp.payment.receipt.read`、`.register`、`.reverse`，`erp.payment.request.read`、`.edit`、`.approve`、`.execute`，`erp.payment.entry.read`、`.edit`、`.approve`，`erp.payment.statement.read`、`.reconcile`，`erp.payment.policy.read`、`.manage`，`erp.payment.flow.decide`、`.manage`；都带法人维度，付款申请另带部门。新建要带 `Idempotency-Key`。审批任务的标准路由由 SDK 登记、条款在线协议第 17 章：`POST /api/payment/flow/tasks/{id}/decisions`（要 `Idempotency-Key`；决定要任务所属流程的批准操作）、`GET /api/payment/flow/tasks/{id}`、`GET /api/payment/flow/tasks?state=paused`（要 `erp.payment.flow.manage`）；原来的 `approve`、`reject` 路由按单据找到调用者的任务再作决定（`Idempotency-Key` 可选），已批准、还没放行的付款申请的驳回是本组件的规则。决定被拒按线协议的原因答（`SELF_APPROVAL`、`NOT_ASSIGNEE`、`TASK_NOT_PENDING`、`VERSION_CONFLICT`、`NOT_QUALIFIED`、`QUALIFICATION_UNKNOWN`、`REVISION_CHANGED`）；放行人是申请人答 `APPROVER_IS_SUBMITTER`；放行核对不过答 409 `NOT_RELEASED`。驳回付款申请时认领由后台工作释放（决定的事务里不调用财务）。
- **事件**（流 `erp.payment`，schema 在 `contract/events/`）：`erp.payment.fund_fact.v1`、`erp.payment.accounting_fact.v1`（增量事实，按 `fact_id` 去重；形状是 `erp/finance` 的共享契约 `fund_fact.v1`、`accounting_fact.v1`），`erp.receipt.changed.v1`、`erp.payment_request.changed.v1`（完整状态）；审批任务 `erp.payment_task.changed.v1`（线协议的共享任务契约 `schemas/flow/task.v1`，按版本覆盖；任务类型 `erp.payment.request.approval`、`erp.payment.entry.approval`）与心跳 `erp.payment_task.heartbeat.v1`；审批待办的通知意图 `erp.payment_notification.intent.v1`（线协议的共享意图契约 `schemas/notification/intent.v1`：任务待处理时发给候选处理人，离开待处理时撤回；部署把本组件列为 `infra/notification` 的发送方时它收）。装了 `mdm/employee` 时订阅它的流 `mdm.employee`（任职、账号关联、员工）。

## 外壳声明

不是外壳。可以被 Go 的核心外壳（`be/go-core`）承载。
