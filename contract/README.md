# erp-payment 的契约

`erp/payment` 发布的契约包：系统面的 protobuf、事件载荷的 JSON Schema、用户面的 OpenAPI。放在组件仓库的 `contract/` 子目录里单独发布（标签 `contract/v3.…`）。

依据：装配项目 [be-assembly-standard](https://github.com/brickKit/be-assembly-standard) 的协议 05（付款认领、资金事实与核销、资金调拨与科目收付）、09（审批与职责分离）、14（单号、金额、列表）；组件的 `docs/design.md`。

## 内容

| 位置 | 是什么 |
| --- | --- |
| `proto/be/erp/payment/v3/payment.proto` | `FactService.Read`（按事实身份读回本组件的增量事实） |
| `gen/go/be/erp/payment/v3/` | Go 代码，包 `paymentv3`；模块 `github.com/brickKit/erp-payment/contract/v3` |
| `python/` | Python 包 `contract-erp-payment`，模块 `be.erp.payment.v3` |
| `events/` | 流 `erp.payment`：资金事实 `erp.payment.fund_fact.v1` 与会计事实 `erp.payment.accounting_fact.v1`（增量事实；schema 只是指向 `erp/finance` 共享契约 `$id` 的 `$ref`，不复制，Go 的测试从财务契约的包 `events` 加载它）；收款单 `erp.receipt.changed.v1`、付款申请 `erp.payment_request.changed.v1`（完整状态） |
| `openapi.yaml` | 用户面；每个路由的操作写在 `x-operation` |

## 要点

- **每笔付给伙伴的钱都针对财务的一个未清项**：付款申请提交时向财务认领、放行时确认，执行后发布带认领清单的资金事实，财务按实付核销。
- **来源不写科目**：事实带过账规则键与金额，财务按法人的映射出分录。
- **金额**是十进制字符串；外币带汇率快照。
