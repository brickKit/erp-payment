# policies：付款政策

按法人的付款政策：付款申请与调拨收付的审批人（`mdm/org` 的岗位或指定个人，至多一个）。付款总要人审批，没有“不要审批”。没设过的法人没有审批人，版本 0：提交答 `NO_APPROVER_CONFIGURED`。

## 负责与不负责

- 负责：表 `payment_policy`；路由 `/api/payment/policies/{legal_entity_id}` 的读取与设置（按版本，版本判断在语句里，两个同版本的修改只成一个）；给别的模块读审批人（`Of`、`Approver`：流程内核的绑定；`ApproverOf`：在自己的事务里，付款申请向财务认领之前用）。
- 不负责：审批本身（SDK 的流程内核，`requests`、`entries` 的 `approval.go`）。

## 文件

| 文件 | 内容 |
| --- | --- |
| `policies.go` | `Policy`、`Approver`、`Of`、`ApproverOf`；`GetPolicy`、`PutPolicy` |

## 依赖与被依赖

- 依赖：`core`。
- 被依赖：`requests`、`entries`（提交时）；入口登记路由。

## 规则在哪

`docs/design.md` 的“拥有的数据”（`payment_policy`）、规则 PR02、PR03、BE02 与未决问题“付款都要人审批”；数据范围只有法人一个维度。

## 测试

`itest/approvals_integration_test.go`（`policies`：起始没有审批人、格式不对的值、看不见答 404、按版本设置；`approvals`：没配审批人什么也不认领、岗位而没有员工组件时暂停）。
