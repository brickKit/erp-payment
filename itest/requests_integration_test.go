//go:build integration

package itest

import (
	"strings"
	"time"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requests: payment proposals and payment requests against erp/finance's open items (its ClaimService stand-in):
// claimed when submitted, approved, released (the claims confirmed), executed with the fund fact, returned,
// rejected, cancelled; the claims renewed while they wait, a lapse back to draft (docs/design.md, PR01-PR08).
func (fx *fixture) requests() {
	t := fx.t
	p1, p2, p3, c1, pu, r1 := ids.New().String(), ids.New().String(), ids.New().String(), ids.New().String(),
		ids.New().String(), ids.New().String()
	due := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	fx.finance.item(p1, "payable", fx.le, fx.supplier, "CNY", "1000.00", due)
	fx.finance.item(p2, "payable", fx.le, fx.supplier, "CNY", "500.00", due)
	fx.finance.item(c1, "credit_balance", fx.le, fx.customer, "CNY", "200.00", due)
	fx.finance.item(pu, "payable", fx.le, fx.supplier, "USD", "300.00", due)
	fx.finance.item(r1, "receivable", fx.le, fx.customer, "CNY", "90.00", due)
	fx.finance.claimFor(p1, "300.00") // another request's pending claim: 700 left on P1

	// the proposal: erp/finance's due payables and credit balances with an available balance
	prop := fx.get("/api/payment/proposals?legal_entity_id=" + fx.le + "&due_on_or_before=" + fx.today())
	got := map[string]string{}
	for _, it := range prop["items"].([]any) {
		got[at(it, "open_item_id").(string)] = at(it, "available").(string)
	}
	assert.Equal(t, map[string]string{p1: "700.00", p2: "500.00", c1: "200.00", pu: "300.00"}, got)
	fx.finance.item(p3, "payable", fx.le, fx.supplier, "CNY", "100.00", time.Now().AddDate(0, 1, 0).Format("2006-01-02"))

	// PR01: a draft; refused: no open item while erp/finance is there, the same item twice, an inactive account, a
	// suspended payee, another legal entity's department
	for _, c := range []struct {
		body         map[string]any
		code         int
		reason, desc string
	}{
		{fx.reqBody("payment", fx.supplier, fx.cny, line("", "10")), 400, "REQUEST_INVALID", "no open item"},
		{fx.reqBody("payment", fx.supplier, fx.cny, line(p1, "10"), line(p1, "5")), 400, "REQUEST_INVALID", "twice"},
		{fx.reqBody("payment", fx.supplier, fx.inactive, line(p1, "10")), 409, "BANK_ACCOUNT_NOT_USABLE", "inactive"},
		{fx.reqBody("payment", fx.blocked, fx.cny, line(p1, "10")), 409, "PARTNER_NOT_USABLE", "suspended"},
		{fx.with(fx.reqBody("payment", fx.supplier, fx.cny, line(p1, "10")), "department_id", ids.New().String()), 409,
			"DEPARTMENT_NOT_USABLE", "department"},
	} {
		code, out := fx.alice.do("POST", "/api/payment/requests", c.body)
		assert.Equal(t, c.code, code, "%s: %v", c.desc, out)
		assert.Equal(t, c.reason, out["reason"], "%s: %v", c.desc, out)
	}
	req := fx.request(fx.alice, fx.reqBody("payment", fx.supplier, fx.cny, line(p2, "100"), line(p1, "800")))
	id := req["id"].(string)
	assert.Equal(t, "draft", req["state"])
	assert.Equal(t, "900", req["amount"])
	assert.Equal(t, "CNY", req["currency"])
	assert.True(t, strings.HasPrefix(req["number"].(string), "PR-SH-"), req["number"])

	// PR02: submitting claims every line; P1 has 700 left, so the whole request stays a draft and P2's claim is
	// released again
	fx.reqRefused(fx.alice, id, "submit", nil, "CLAIM_EXCEEDS_AVAILABLE")
	assert.Equal(t, "draft", fx.get("/api/payment/requests/" + id)["state"])
	assert.Equal(t, "500.00", fx.finance.available(p2), "the claim already made is released")
	code, out := fx.alice.do("PATCH", "/api/payment/requests/"+id, fx.with(fx.reqBody("payment", fx.supplier, fx.cny,
		line(p1, "700"), line(p2, "100")), "version", req["version"]))
	require.Equal(t, 200, code, out)
	req = fx.reqAction(fx.alice, id, "submit", nil)
	assert.Equal(t, "submitted", req["state"])
	assert.Equal(t, sectest.Alice, req["applicant_id"])
	claims := []string{at(req, "lines", 0, "claim_id").(string), at(req, "lines", 1, "claim_id").(string)}
	assert.Equal(t, "pending", fx.finance.state(claims[0]))
	assert.NotNil(t, at(req, "lines", 0, "claim_expires_at"))
	assert.Equal(t, "0.00", fx.finance.available(p1))
	code, out = fx.alice.do("PATCH", "/api/payment/requests/"+id, fx.reqBody("payment", fx.supplier, fx.cny, line(p1, "1")))
	assert.Equal(t, 409, code, "a submitted request is not edited (PR01): %v", out)

	// PR03, PR04: the applicant neither approves nor releases; Bob does; releasing confirms the claims at finance
	fx.reqRefused(fx.alice, id, "approve", nil, "APPROVER_IS_SUBMITTER")
	assert.Equal(t, "approved", fx.reqAction(fx.bob, id, "approve", nil)["state"])
	fx.reqRefused(fx.alice, id, "release", nil, "APPROVER_IS_SUBMITTER")
	req = fx.reqAction(fx.bob, id, "release", nil)
	assert.Equal(t, "ready", req["state"])
	assert.Equal(t, "confirmed", fx.finance.state(claims[0]))
	assert.Equal(t, "confirmed", fx.finance.state(claims[1]))
	assert.Nil(t, at(req, "lines", 0, "claim_expires_at"), "a confirmed claim does not expire")

	// PR05: executed: 750 of 800 paid, split in the lines' order (700 to P1, 50 to P2); the fund fact carries each
	// claim with its paid amount; a second execution is ignored and publishes nothing more
	exec := map[string]any{"paid_amount": "750", "paid_on": fx.today(), "bank_reference": "P-1"}
	code, out = fx.alice.do("POST", "/api/payment/requests/"+id+"/execute", fx.with(exec, "paid_amount", "801"))
	assert.Equal(t, 400, code, "more than requested: %v", out)
	req = fx.reqAction(fx.alice, id, "execute", exec)
	assert.Equal(t, "paid", req["state"])
	assert.Equal(t, "750", req["paid_amount"])
	assert.Equal(t, "700", at(req, "lines", 0, "paid"))
	assert.Equal(t, "50", at(req, "lines", 1, "paid"))
	f := fx.fact(req["fact_id"].(string), core.FundFact)
	fx.valid(core.FundFact, f)
	assert.Equal(t, "payment", f["kind"])
	assert.Equal(t, "payment", f["rule_key"])
	assert.Equal(t, fx.supplier, f["partner_id"])
	assert.Equal(t, "750", f["amount"])
	assert.Equal(t, fx.d1, at(f, "dimensions", "department_id"))
	assert.Equal(t, []any{map[string]any{"claim_id": claims[0], "amount": "700"}, map[string]any{"claim_id": claims[1],
		"amount": "50"}}, f["claims"])
	assert.Equal(t, "paid", fx.reqAction(fx.alice, id, "execute", fx.with(exec, "paid_amount", "1"))["state"])
	assert.Equal(t, 1, fx.factCount(core.FundFact, "document_id", id), "one execution, one fact")
	fx.valid(core.RequestChanged, fx.factWhere(core.RequestChanged, "id", id))

	// PR06: the bank returned the money: the reversing fact refers to the payment's
	fx.reqRefused(fx.bob, id, "return", map[string]any{}, "REQUEST_INVALID")
	req = fx.reqAction(fx.bob, id, "return", map[string]any{"reason": "收款账户已销户，银行退回"})
	assert.Equal(t, "returned", req["state"])
	fr := fx.factWhere(core.FundFact, "reverses_fact_id", f["fact_id"].(string))
	fx.valid(core.FundFact, fr)
	assert.Equal(t, "reversal", fr["kind"])
	assert.Equal(t, "payment", fr["rule_key"])
	assert.Equal(t, "750", fr["amount"])
	fx.reqRefused(fx.alice, id, "cancel", nil, "STATE_CONFLICT")

	// PR03: a refund of a credit balance rejected back to draft: its claim released; submitted again under a new claim
	ref := fx.request(fx.alice, fx.reqBody("refund", fx.customer, fx.cny, line(c1, "150")))
	rid := ref["id"].(string)
	ref = fx.reqAction(fx.alice, rid, "submit", nil)
	first := at(ref, "lines", 0, "claim_id").(string)
	fx.reqRefused(fx.bob, rid, "reject", map[string]any{}, "REQUEST_INVALID")
	ref = fx.reqAction(fx.bob, rid, "reject", map[string]any{"reason": "退款金额要和客户再确认"})
	assert.Equal(t, "draft", ref["state"])
	assert.Equal(t, "退款金额要和客户再确认", ref["reason"])
	assert.Equal(t, "released", fx.finance.state(first))
	assert.Nil(t, at(ref, "lines", 0, "claim_id"))
	ref = fx.reqAction(fx.alice, rid, "submit", nil)
	assert.NotEqual(t, first, at(ref, "lines", 0, "claim_id"), "a new submission, a new claim")

	// PR07: cancelled after its release: the confirmed claim released by its user
	fx.reqAction(fx.bob, rid, "approve", nil)
	ref = fx.reqAction(fx.bob, rid, "release", nil)
	second := at(ref, "lines", 0, "claim_id").(string)
	assert.Equal(t, "cancelled", fx.reqAction(fx.alice, rid, "cancel", nil)["state"])
	assert.Equal(t, "released", fx.finance.state(second))
	assert.Equal(t, "200.00", fx.finance.available(c1))
	fx.reqRefused(fx.bob, rid, "approve", nil, "STATE_CONFLICT")

	// PR02: an open item not paid out to this payee in this kind and currency is refused before any claim: a
	// receivable, another partner's payable, a payable on a refund, the USD payable on a CNY account; a USD account
	// pays the USD payable with the rate snapshot, its paid amount given by line
	for _, b := range []map[string]any{fx.reqBody("refund", fx.customer, fx.cny, line(r1, "10")),
		fx.reqBody("payment", fx.customer, fx.cny, line(p2, "10")), fx.reqBody("refund", fx.supplier, fx.cny, line(p2, "10")),
		fx.reqBody("payment", fx.supplier, fx.cny, line(pu, "10"))} {
		bad := fx.request(fx.alice, b)
		fx.reqRefused(fx.alice, bad["id"].(string), "submit", nil, "OPEN_ITEM_NOT_PAYABLE")
		fx.reqAction(fx.alice, bad["id"].(string), "cancel", nil)
	}
	// the first request's confirmed 100 on P2 stays held at the stand-in (it does not settle fund facts): 400 is unchanged
	assert.Equal(t, "400.00", fx.finance.available(p2), "nothing was claimed")
	usd := fx.request(fx.alice, fx.with(fx.reqBody("payment", fx.supplier, fx.usd, line(pu, "300")), "department_id", nil))
	uid := usd["id"].(string)
	fx.reqAction(fx.alice, uid, "submit", nil)
	fx.reqAction(fx.bob, uid, "approve", nil)
	usd = fx.reqAction(fx.bob, uid, "release", nil)
	usd = fx.reqAction(fx.alice, uid, "execute", map[string]any{"paid_amount": "120", "paid_on": fx.today(),
		"bank_reference": "P-2", "lines": []any{map[string]any{"line_id": at(usd, "lines", 0, "id"), "paid": "120"}}})
	fu := fx.fact(usd["fact_id"].(string), core.FundFact)
	assert.Equal(t, "USD", fu["currency"])
	assert.Equal(t, map[string]any{"rate": "7.1", "date": fx.today(), "type": "spot"}, fu["exchange_rate"])
	assert.Nil(t, at(fu, "dimensions", "department_id"))

	// PR04: a claim lost at the release answers CLAIM_LOST: the claims confirmed before it are released and the
	// request goes back to draft with the reason (nothing stays held at finance)
	lost := fx.request(fx.alice, fx.reqBody("payment", fx.supplier, fx.cny, line(p2, "40"), line(p3, "10")))
	lid := lost["id"].(string)
	lost = fx.reqAction(fx.alice, lid, "submit", nil)
	fx.reqAction(fx.bob, lid, "approve", nil)
	confirmedFirst := at(lost, "lines", 0, "claim_id").(string)
	fx.finance.lose(at(lost, "lines", 1, "claim_id").(string))
	fx.reqRefused(fx.bob, lid, "release", nil, "CLAIM_LOST")
	lost = fx.get("/api/payment/requests/" + lid)
	assert.Equal(t, "draft", lost["state"])
	assert.Equal(t, "付款认领已失效", lost["reason"])
	assert.Equal(t, "released", fx.finance.state(confirmedFirst), "the claim confirmed before the lost one is released")
	assert.Equal(t, "100.00", fx.finance.available(p3))

	// PR08: waiting claims are renewed before their end; a renewal refused (the claim lapsed) takes the request back to
	// draft with the reason, its other claims released
	fx.finance.mu.Lock()
	fx.finance.period = 4 * time.Second
	fx.finance.mu.Unlock()
	kept := fx.request(fx.alice, fx.reqBody("payment", fx.supplier, fx.cny, line(p2, "30")))
	kid := kept["id"].(string)
	fx.reqAction(fx.alice, kid, "submit", nil)
	gone := fx.request(fx.alice, fx.reqBody("payment", fx.supplier, fx.cny, line(p2, "20"), line(p3, "5")))
	gid := gone["id"].(string)
	gone = fx.reqAction(fx.alice, gid, "submit", nil)
	fx.finance.lose(at(gone, "lines", 0, "claim_id").(string))
	require.Eventually(t, func() bool { return fx.get("/api/payment/requests/" + gid)["state"] == "draft" }, 20*time.Second,
		200*time.Millisecond, "the lapse takes the request back to draft")
	gone = fx.get("/api/payment/requests/" + gid)
	assert.Equal(t, "付款认领已失效", gone["reason"])
	assert.Equal(t, "100.00", fx.finance.available(p3), "the other claim is released")
	time.Sleep(5 * time.Second)
	kept = fx.get("/api/payment/requests/" + kid)
	assert.Equal(t, "submitted", kept["state"], "renewed past its first end")
	assert.Equal(t, "pending", fx.finance.state(at(kept, "lines", 0, "claim_id").(string)))
	assert.Positive(t, fx.finance.renewCount())
	fx.finance.mu.Lock()
	fx.finance.period = time.Hour
	fx.finance.mu.Unlock()

	// the list by state; Carol may not read payment requests (404)
	page := fx.get("/api/payment/requests?legal_entity_id=" + fx.le + "&state=draft")
	assert.Len(t, page["requests"], 2, page) // the lapsed one and the one lost at its release (the refused ones were cancelled)
	code, _ = fx.carol.do("GET", "/api/payment/requests/"+id, nil)
	assert.Equal(t, 404, code)
}

func line(item, amount string) map[string]any {
	l := map[string]any{"amount": amount}
	if item != "" {
		l["open_item_id"] = item
	}
	return l
}

func (fx *fixture) reqBody(kind, payee, account string, lines ...map[string]any) map[string]any {
	var ls []any
	for _, l := range lines {
		ls = append(ls, l)
	}
	return map[string]any{"legal_entity_id": fx.le, "kind": kind, "payee_id": payee, "bank_account_id": account,
		"purpose": "货款", "reference": "SI-ref", "department_id": fx.d1, "lines": ls}
}

func (fx *fixture) request(c client, body map[string]any) map[string]any {
	fx.t.Helper()
	code, out := c.do("POST", "/api/payment/requests", body)
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) reqAction(c client, id, action string, body map[string]any) map[string]any {
	fx.t.Helper()
	code, out := c.do("POST", "/api/payment/requests/"+id+"/"+action, body)
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) reqRefused(c client, id, action string, body map[string]any, reason string) {
	fx.t.Helper()
	_, out := c.do("POST", "/api/payment/requests/"+id+"/"+action, body)
	assert.Equal(fx.t, reason, out["reason"], "%s: %v", action, out)
}

// factCount counts the outbox's events of a type whose source document is id.
func (fx *fixture) factCount(typ, key, id string) int {
	fx.t.Helper()
	var n int
	require.NoError(fx.t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".be_outbox WHERE type = $1 AND "+
		"convert_from(payload, 'UTF8')::jsonb->'source'->>'"+key+"' = $2", typ, id).Scan(&n))
	return n
}
