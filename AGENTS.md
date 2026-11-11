# erp/payment

收付款组件的代码。用法与边界在 `BRICKKIT.md`，为什么这样设计（术语、数据、状态、规则、未决问题）在 [docs/design.md](docs/design.md)。跨组件的规则在装配项目 be-assembly-standard 的协议 05（付款认领、资金事实与核销、资金调拨与科目收付）、协议 09（审批与职责分离）、协议 14（单号、金额、列表）、协议 02（部门投影）、安全 07（维度与受限字段）与决定 D47。

## 代码地图

代码按业务模块分层。**先读这一节，再进目录**：每个代码目录都有自己的 `AGENTS.md`（同目录的 `CLAUDE.md` 让 Claude Code 读这个目录的文件时自动带上它），写这一层负责什么、不负责什么、每个文件、依赖谁、被谁用、测在哪；读完就知道要不要往下走。

| 目录 | 是什么 |
| --- | --- |
| [`payment/`](payment/AGENTS.md) | 入口：组件定义与组装（路由、订阅、后台工作都在这里登记，转给业务模块）；不写业务。`main.go` 与外壳只引用它 |
| [`internal/`](internal/AGENTS.md) | 业务：下面的表里每个模块一个包；模块之间的依赖表在它的 `AGENTS.md`。Go 的 `internal` 让别的组件编译期就引用不了（决定 0101） |
| [`itest/`](itest/AGENTS.md) | 集成测试，按业务流程分文件；它的 `AGENTS.md` 是测试索引（文件 → 流程 → 规则编号） |
| `payment/migrations/` | 表，整个组件一套（迁移顺序是全组件的，不按模块拆） |
| `contract/` | 本组件发布的契约，单独的模块：`contract/proto/`、`contract/events/`（资金事实与会计事实只引用 `erp/finance` 的共享契约）、`contract/openapi.yaml` |
| `docs/` | 设计（规则的正文；各层的 `AGENTS.md` 只指过去，不抄） |

业务模块（`internal/` 下）：

| 模块 | 负责 | 拥有的表 |
| --- | --- | --- |
| [`facts`](internal/facts/AGENTS.md) | 系统面 `FactService.Read` | `payment_fact`（写入在 `core.Fact`） |
| [`orgunits`](internal/orgunits/AGENTS.md) | 部门投影（协议 02）：部门路径复制到付款申请上 | `org_realign`（投影本身在 SDK 的表里） |

模块共用的（不属于某一个业务）：[`masterdata`](internal/masterdata/AGENTS.md)（事务外取证：主数据，以后还有财务的认领、销售的订单、汇率）、[`core`](internal/core/AGENTS.md)（名字、资源与操作的定义、错误与校验、生命周期表的通用写法、增量事实 `Fact`）。

**实现进度**（3.0.0 开发中，装配项目阶段计划 C3 的 P3）：骨架；路由先都答“能力不支持”，按 P3b–P3e 逐个做。加一个模块就在上面两张表里加一行。

| 想改 | 从这里开始 | 然后 |
| --- | --- | --- |
| 一个维度、一个操作、列表能筛选的字段 | `internal/core/names.go` | `docs/design.md` 的命令与查询、`BRICKKIT.md` 的契约索引；路由的登记在 `payment/module.go` |
| 加字段、加接口、改事件 | 装配项目 be-assembly-standard 规范里的“改动配方” | 资金事实与会计事实的形状属于 `erp/finance` 的共享契约，要改先改那边 |

## 构建与测试

- `go test ./...`（`make test`）：单元测试，在 `payment/contract_test.go`：清单的 `configSchema` 与 `events`、OpenAPI 与路由、合并安全。
- `make test-integration`（`go test -tags integration ./itest/`）：真实的 PostgreSQL 与消息系统；每个文件测哪条流程、覆盖哪些规则、要什么前提，在 [itest/AGENTS.md](itest/AGENTS.md)。
- `brickkit lint`

## 设计取舍

- **每笔付给伙伴的钱都针对财务的一个未清项**：认领在财务（额度的属主），本组件提交时认领、放行时确认、执行后发布带认领清单的资金事实；财务在核销的事务里正式化。
- **来源不写科目**：事实带规则键与金额，财务按法人的映射出分录。
- **部门路径复制到单据上**（同 `erp/finance`）：SDK 的列表只按本表的列下推组织树维度，所以付款申请带 `department_path`，部门事件到达时改写。

## 易错点

- 不要在事务里调财务、销售、主数据或取汇率：先取证（`internal/masterdata`），再开事务；认领、确认、释放、续期都按身份幂等，失败重试安全。
- 不要用浮点算金额：比较与加减经 `core.Sum`、`core.Minus`、`core.Cmp`（`apd`）或在 SQL 里做。
- 带 `department_path` 的新表要加进 `core.DepartmentTables`，否则部门移动时它的副本不改写。

## 改代码前自查

- 改了契约（`contract/`）：proto 改后 `make -C contract gen`，发布 `contract/v…`，再改这里的依赖；OpenAPI 与路由由测试核对。
- 改了版本号吗？每次改动都升版本。

<!-- brickkit:managed:begin lang=zh -->
## BrickKit

这是一个 BrickKit 组件：平台只读 `component.yaml`。它依赖的规则：

- `configSchema` 的键就是代码读的环境变量名。不能用保留名：`COMPONENT_ID`、`COMPONENT_VERSION`、`PORT`、`BRICKKIT_SERVED_MEMBERS`、`BRICKKIT_SERVED_MEMBERS_CONFIG`，以及任何 `*_ENDPOINT`。
- 依赖写精确版本。依赖的地址以 `<ID>_ENDPOINT` 注入；缺席的可选依赖根本没有这个变量，读的时候要带兜底。
- `/healthz` 只查本进程，不查依赖。迁移命令用同一个镜像跑，遇到不认识的参数必须直接失败；`local.migrationCommand` 从源码跑同一条迁移，给 `mode: local` 用。
- `BRICKKIT.md` 会随版本进入每个使用它的项目，在那里是脱离仓库单独读的：跟代码一起改，不放相对链接。
- 发版：改 `metadata.version`，提交、推送，`brickkit release`。`brickkit lint` 会检查清单和这些文档——在项目里、在这个目录下跑，只查这个组件（`--all` 查整个项目）。
- 完整规则在 `brickkit-component` 技能里（装了技能的项目或仓库根目录下的 `.claude/skills/brickkit-component/SKILL.md`；`brickkit skills update` 会装上）；参数问 `brickkit <命令> --help`；BrickKit 自己的文档用 `brickkit docs` 看。
<!-- brickkit:managed:end -->
