# masterdata：事务外取证（共用）

本组件在事务之前向别的组件取的证据：法人（本位币、代码、时区）、部门、伙伴、币种的小数位、外币的汇率快照、销售的订单；财务的认领服务（到期未清项、认领、续期、确认、释放）。每个调用都在事务外（协议 P4.20）：先取证，再开事务。

## 负责与不负责

- 负责：把上游契约的回答变成本组件要的形状；不存在、不可用答本组件的原因（`LEGAL_ENTITY_NOT_USABLE`、`PARTNER_NOT_USABLE`、`CURRENCY_NOT_USABLE`（含没有汇率）、`ORDER_NOT_USABLE`（含没装销售）），连不上答 `DEPENDENCY_UNAVAILABLE`（不当作“不存在”，协议 07）；可选依赖装没装（`Deployed`）。
- 不负责：写库；业务规则。

## 文件

| 文件 | 内容 |
| --- | --- |
| `upstream.go` | `Conn`、`LegalEntityOf`、`OrgUnitRead`（部门投影用）、`PartnerOf`、`CurrencyScale`、`Deployed`、`Rate` 与 `RateOf`（`mdm/currency` 的 `RateService`，按业务日期的即期汇率；同一币种时为空）、`Order` 与 `OrderOf`（`erp/sales` 的 `OrderService.Read`）；财务的 `ClaimService`：`DueItems`、`Claim`、`Renew`、`Confirm`、`Release`、`ClaimState`（`Get`，财务不认识的答空）（财务拒绝时转成本组件同名的原因 `CLAIM_EXCEEDS_AVAILABLE`、`CLAIM_CONFLICT`、`CLAIM_STATE`，`core.ClaimRefused` 判断；释放一个财务不认识的认领算已释放） |

## 依赖与被依赖

- 依赖：`core`；契约 `mdm/org`、`mdm/partner`、`mdm/currency`、`erp/sales`、`erp/finance`。
- 被依赖：各业务模块、`orgunits`。

## 规则在哪

调用别的组件：装配项目的“调用其他组件”（事务内不发网络调用）；依赖缺席时的做法：`docs/design.md` 的“依赖”。

## 测试

由流程测试经替身覆盖（`itest/standins_integration_test.go`）。
