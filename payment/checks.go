package payment

import (
	core "github.com/brickKit/erp-payment/v3/internal/core"
	requests "github.com/brickKit/erp-payment/v3/internal/requests"

	"github.com/brickKit/be-sdk-go/v3/recovery"
)

// RecoveryChecks registers what a full recovery asks this member (be-protocol chapter 14; docs/design.md, "整套恢复
// 之后"): whether the objects its audit names are still there and at which version, and its own check (the released
// requests' claims at erp/finance). Its numbers are gapped series: nothing to void.
func (m *Module) RecoveryChecks(c *recovery.Checks) error {
	c.Presence(core.BankAccounts, recovery.TablePresence("bank_account", "id", "version"))
	c.Presence(core.Receipts, recovery.TablePresence("receipt", "id", "version"))
	c.Presence(core.Requests, recovery.TablePresence("payment_request", "id", "version"))
	c.Presence(core.Entries, recovery.TablePresence("bank_entry", "id", "version"))
	c.Presence(core.Statements, recovery.TablePresence("statement_line", "id", "version"))
	return c.Add(recovery.Check{Name: requests.CheckHeldClaims, Basis: "装配项目协议 05 的付款认领；协议 15 第 8 步",
		Run: requests.HeldClaims(m.b)})
}
