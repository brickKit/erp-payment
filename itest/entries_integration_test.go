//go:build integration

package itest

import (
	"strings"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entries: transfers between the company's accounts (going out into the money in transit, arriving later), bank
// charges and interest, approved and posted as accounting facts (docs/design.md, BE01-BE04; protocol 05).
func (fx *fixture) entries() {
	t := fx.t
	icbc := fx.account(fx.le, "工行户", "9558 8001 0000 1234", "CNY")["id"].(string)

	// BE01: refused: a transfer to an account of another currency, to itself, to an inactive one, to another legal
	// entity's; a charge naming a to-account; a draft changed by version, a submitted one not
	for _, c := range []struct {
		body   map[string]any
		code   int
		reason string
	}{
		{fx.entryBody("transfer", fx.cny, fx.usd, "100"), 400, "REQUEST_INVALID"},
		{fx.entryBody("transfer", fx.cny, fx.cny, "100"), 400, "REQUEST_INVALID"},
		{fx.entryBody("transfer", fx.cny, fx.inactive, "100"), 409, "BANK_ACCOUNT_NOT_USABLE"},
		{fx.entryBody("transfer", fx.cny, fx.bj, "100"), 409, "BANK_ACCOUNT_NOT_USABLE"},
		{fx.entryBody("charge", fx.cny, icbc, "20"), 400, "REQUEST_INVALID"},
		{fx.entryBody("charge", fx.inactive, "", "20"), 409, "BANK_ACCOUNT_NOT_USABLE"},
	} {
		code, out := fx.alice.do("POST", "/api/payment/entries", c.body)
		assert.Equal(t, c.code, code, "%v: %v", c.body, out)
		assert.Equal(t, c.reason, out["reason"], "%v: %v", c.body, out)
	}

	// BE02: a bank charge: the maker does not approve; approved, it posts its accounting fact
	charge := fx.entry(fx.alice, fx.entryBody("charge", fx.cny, "", "20"))
	cid := charge["id"].(string)
	assert.Equal(t, "draft", charge["state"])
	assert.True(t, strings.HasPrefix(charge["number"].(string), "BE-SH-"), charge["number"])
	code, out := fx.alice.do("PATCH", "/api/payment/entries/"+cid, fx.with(fx.entryBody("charge", fx.cny, "", "25"), "version", 1))
	require.Equal(t, 200, code, out)
	assert.Equal(t, "25", out["amount"])
	fx.entryAction(fx.alice, cid, "submit", nil)
	code, out = fx.alice.do("PATCH", "/api/payment/entries/"+cid, fx.entryBody("charge", fx.cny, "", "26"))
	assert.Equal(t, 409, code, "a submitted entry is not changed (BE01): %v", out)
	fx.entryRefused(fx.alice, cid, "approve", nil, "APPROVER_IS_SUBMITTER")
	charge = fx.entryAction(fx.bob, cid, "approve", nil)
	assert.Equal(t, "posted", charge["state"])
	f := fx.factWhere(core.AccountingFact, "rule_key", "bank_charge")
	fx.valid(core.AccountingFact, f)
	assert.Equal(t, map[string]any{"expense": "25", "bank": "25"}, f["amounts"])
	assert.Equal(t, cid, at(f, "source", "document_id"))
	assert.Equal(t, "CNY", f["currency"])
	fx.entryRefused(fx.alice, cid, "cancel", nil, "STATE_CONFLICT")

	// BE02: interest rejected back to draft with the reason, submitted again, posted; a USD one with its rate snapshot
	interest := fx.entry(fx.alice, fx.entryBody("interest", fx.usd, "", "3.5"))
	iid := interest["id"].(string)
	fx.entryAction(fx.alice, iid, "submit", nil)
	fx.entryRefused(fx.bob, iid, "reject", map[string]any{}, "REQUEST_INVALID")
	interest = fx.entryAction(fx.bob, iid, "reject", map[string]any{"reason": "利息金额与对账单不符"})
	assert.Equal(t, "draft", interest["state"])
	assert.Equal(t, "利息金额与对账单不符", interest["reason"])
	fx.entryAction(fx.alice, iid, "submit", nil)
	fx.entryAction(fx.bob, iid, "approve", nil)
	fi := fx.factWhere(core.AccountingFact, "rule_key", "bank_interest")
	fx.valid(core.AccountingFact, fi)
	assert.Equal(t, map[string]any{"bank": "3.5", "income": "3.5"}, fi["amounts"])
	assert.Equal(t, map[string]any{"rate": "7.1", "date": fx.today(), "type": "spot"}, fi["exchange_rate"])

	// BE02, BE03: a transfer goes out into the money in transit, then arrives; both facts come from the transfer
	tr := fx.entry(fx.alice, fx.entryBody("transfer", fx.cny, icbc, "1000"))
	tid := tr["id"].(string)
	fx.entryAction(fx.alice, tid, "submit", nil)
	tr = fx.entryAction(fx.bob, tid, "approve", nil)
	assert.Equal(t, "in_transit", tr["state"])
	out1 := fx.factWhere(core.AccountingFact, "rule_key", "fund_transfer_out")
	fx.valid(core.AccountingFact, out1)
	assert.Equal(t, map[string]any{"in_transit": "1000", "bank": "1000"}, out1["amounts"])
	fx.entryRefused(fx.alice, tid, "cancel", nil, "STATE_CONFLICT")
	fx.entryRefused(fx.alice, tid, "arrive", map[string]any{"arrived_on": "2000-01-01"}, "REQUEST_INVALID")
	tr = fx.entryAction(fx.alice, tid, "arrive", map[string]any{"arrived_on": fx.today()})
	assert.Equal(t, "posted", tr["state"])
	assert.Equal(t, fx.today(), tr["arrived_on"])
	in1 := fx.factWhere(core.AccountingFact, "rule_key", "fund_transfer_in")
	fx.valid(core.AccountingFact, in1)
	assert.Equal(t, map[string]any{"bank": "1000", "in_transit": "1000"}, in1["amounts"])
	assert.Equal(t, tid, at(in1, "source", "document_id"))
	assert.Equal(t, tid, at(out1, "source", "document_id"))
	fx.entryAction(fx.alice, tid, "arrive", map[string]any{"arrived_on": fx.today()}) // a repeat is ignored
	assert.Equal(t, 2, fx.factCount(core.AccountingFact, "document_id", tid), "one going out, one arrival")

	// BE04: a draft is cancelled; the list by kind
	gone := fx.entry(fx.alice, fx.entryBody("charge", fx.cny, "", "1"))
	assert.Equal(t, "cancelled", fx.entryAction(fx.alice, gone["id"].(string), "cancel", nil)["state"])
	page := fx.get("/api/payment/entries?legal_entity_id=" + fx.le + "&kind=charge")
	assert.Len(t, page["entries"], 2, page)
	fx.icbc = icbc
}

func (fx *fixture) entryBody(kind, account, to, amount string) map[string]any {
	b := map[string]any{"legal_entity_id": fx.le, "kind": kind, "bank_account_id": account, "amount": amount,
		"entry_date": fx.today(), "description": "「测试」" + kind}
	if to != "" {
		b["to_bank_account_id"] = to
	}
	return b
}

func (fx *fixture) entry(c client, body map[string]any) map[string]any {
	fx.t.Helper()
	code, out := c.do("POST", "/api/payment/entries", body)
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) entryAction(c client, id, action string, body map[string]any) map[string]any {
	fx.t.Helper()
	code, out := c.do("POST", "/api/payment/entries/"+id+"/"+action, body)
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) entryRefused(c client, id, action string, body map[string]any, reason string) {
	fx.t.Helper()
	_, out := c.do("POST", "/api/payment/entries/"+id+"/"+action, body)
	assert.Equal(fx.t, reason, out["reason"], "%s: %v", action, out)
}
