//go:build integration

package itest

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statements: bank statement lines registered, matched to receipts, payments and entries (automatically by the bank
// reference, by hand, from the document's side), differences handled, a bank charge made from a line, the account's
// reconciliation (docs/design.md, BR01-BR04).
func (fx *fixture) statements() {
	t := fx.t
	// BR01: registered and matched once: R-1 (the one received; the reversed R-1 does not match) and R-3 by their
	// bank references; P-1 was returned, it does not match; X-9 has no receipt yet; lines without a reference stay
	reg := fx.lines(fx.cny, stLine("1000", "R-1", "货款"), stLine("300", "R-3", "预收"), stLine("-750", "P-1", "付款"),
		stLine("500", "X-9", "来款"), stLine("-25", "", "手续费"), stLine("-8", "", "短信费"), stLine("12.34", "", "未知"))
	lines := reg["lines"].([]any)
	require.Len(t, lines, 7, reg)
	assert.Equal(t, "matched", at(lines[0], "state"))
	assert.Equal(t, "receipt", at(lines[0], "matched", "document_type"))
	assert.Equal(t, "matched", at(lines[1], "state"))
	for _, i := range []int{2, 3, 4, 5, 6} {
		assert.Equal(t, "unmatched", at(lines[i], "state"), "line %d", i)
	}
	ids := make([]string, len(lines))
	for i, l := range lines {
		ids[i] = at(l, "id").(string)
	}
	again := fx.lines(fx.cny, stLine("1000", "R-1", "重复"), stLine("1", "", "无号"))
	assert.Equal(t, []any{"R-1"}, again["skipped"], "a reference registered on the account is skipped")
	assert.Len(t, again["lines"], 1, "a line without a reference is always new")

	// BR01: a receipt registered later finds its line
	x := fx.receipt(fx.alice, map[string]any{"bank_account_id": fx.cny, "amount": "500", "bank_reference": "X-9",
		"payer_id": fx.customer}, "")
	l := fx.line(ids[3])
	assert.Equal(t, "matched", l["state"])
	assert.Equal(t, x["id"], at(l, "matched", "document_id"))

	// BR02: by hand: the charge of 25; refused: another amount, a document matched already, another account's
	fx.lineRefused(ids[4], "match", map[string]any{"document_type": "bank_entry", "document_id": fx.transfer}, "NOT_MATCHABLE")
	fx.lineRefused(ids[6], "match", map[string]any{"document_type": "receipt", "document_id": x["id"]}, "NOT_MATCHABLE")
	l = fx.lineAction(ids[4], "match", map[string]any{"document_type": "bank_entry", "document_id": fx.charge})
	assert.Equal(t, "matched", l["state"])
	assert.Equal(t, fx.charge, at(l, "matched", "document_id"))
	fx.lineRefused(ids[6], "match", map[string]any{"document_type": "bank_entry", "document_id": fx.charge}, "NOT_MATCHABLE")
	assert.Equal(t, "unmatched", fx.lineAction(ids[4], "unmatch", nil)["state"])
	fx.lineAction(ids[4], "match", map[string]any{"document_type": "bank_entry", "document_id": fx.charge})
	// the same amount refused when the document is matched by another line, or is on another account
	twin := fx.lines(fx.cny, stLine("-25", "", "又一笔手续费"))["lines"].([]any)[0]
	fx.lineRefused(at(twin, "id").(string), "match", map[string]any{"document_type": "bank_entry", "document_id": fx.charge}, "NOT_MATCHABLE")
	fx.lineAction(at(twin, "id").(string), "resolve", map[string]any{"reason": "银行重复列示"})
	r4 := at(fx.get("/api/payment/receipts?legal_entity_id="+fx.le+"&q=R-4"), "receipts", 0, "id").(string)
	other := fx.lines(fx.icbc, stLine("88", "", "工行来款"))["lines"].([]any)[0]
	fx.lineRefused(at(other, "id").(string), "match", map[string]any{"document_type": "receipt", "document_id": r4}, "NOT_MATCHABLE")

	// BR02: a transfer matches one line on each account: going out on the CNY account, arrived on ICBC's
	out := fx.lines(fx.cny, stLine("-1000", "", "调拨转出"))["lines"].([]any)[0]
	in := fx.lines(fx.icbc, stLine("1000", "", "调拨转入"))["lines"].([]any)[0]
	fx.lineAction(at(out, "id").(string), "match", map[string]any{"document_type": "bank_entry", "document_id": fx.transfer})
	fx.lineAction(at(in, "id").(string), "match", map[string]any{"document_type": "bank_entry", "document_id": fx.transfer})

	// BR04: a bank charge from a line: a draft with the line's amount and date; approved, it matches its line
	e := fx.entryFromLine(ids[5])
	assert.Equal(t, "charge", e["kind"])
	assert.Equal(t, "8", e["amount"])
	assert.Equal(t, ids[5], e["statement_line_id"])
	fx.lineRefused(ids[5], "create-entry", nil, "STATE_CONFLICT") // not twice: the line has its draft
	fx.entryAction(fx.alice, e["id"].(string), "submit", nil)
	fx.entryAction(fx.bob, e["id"].(string), "approve", nil)
	assert.Equal(t, "matched", fx.line(ids[5])["state"])

	// BR03: a difference handled with a note, reopened
	fx.lineRefused(ids[2], "resolve", map[string]any{}, "REQUEST_INVALID")
	l = fx.lineAction(ids[2], "resolve", map[string]any{"reason": "付款被银行退回，退款另行入账"})
	assert.Equal(t, "resolved", l["state"])
	assert.Equal(t, "付款被银行退回，退款另行入账", l["note"])
	fx.lineRefused(ids[2], "unmatch", nil, "STATE_CONFLICT")
	assert.Equal(t, "unmatched", fx.lineAction(ids[2], "reopen", nil)["state"])

	// the reconciliation: the lines' totals; this side's documents no line matched (the unknown payer's 88, the USD
	// account's are on their own account)
	r := fx.get("/api/payment/bank-accounts/" + fx.cny + "/reconciliation?from=" + fx.today() + "&to=" + fx.today())
	assert.Equal(t, "5.34", r["statement_total"], r)    // the resolved twin -25 too
	assert.Equal(t, "767", r["matched_total"], r)       // 1000 + 300 + 500 - 25 - 8 - 1000
	assert.Equal(t, "-761.66", r["unmatched_total"], r) // -750 + 12.34 + 1 - 25 (resolved)
	numbers := map[string]bool{}
	for _, d := range r["unmatched_documents"].([]any) {
		numbers[at(d, "document_type").(string)+" "+at(d, "amount").(string)] = true
	}
	assert.True(t, numbers["receipt 88"], r)
	assert.False(t, numbers["receipt 500"], "matched")
	page := fx.get("/api/payment/statement-lines?bank_account_id=" + fx.cny + "&state=unmatched")
	assert.Len(t, page["lines"], 3, page) // -750, 12.34, 1
}

func stLine(amount, ref, text string) map[string]any {
	l := map[string]any{"booked_on": nil, "amount": amount, "text": text}
	if ref != "" {
		l["bank_reference"] = ref
	}
	return l
}

func (fx *fixture) lines(account string, ls ...map[string]any) map[string]any {
	fx.t.Helper()
	var body []any
	for _, l := range ls {
		l["booked_on"] = fx.today()
		body = append(body, l)
	}
	code, out := fx.alice.do("POST", "/api/payment/statement-lines", map[string]any{"bank_account_id": account, "lines": body})
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) line(id string) map[string]any {
	fx.t.Helper()
	page := fx.get("/api/payment/statement-lines?bank_account_id=" + fx.cny + "&limit=200")
	for _, l := range page["lines"].([]any) {
		if at(l, "id") == id {
			return l.(map[string]any)
		}
	}
	fx.t.Fatalf("no line %s", id)
	return nil
}

func (fx *fixture) lineAction(id, action string, body map[string]any) map[string]any {
	fx.t.Helper()
	code, out := fx.alice.do("POST", "/api/payment/statement-lines/"+id+"/"+action, body)
	require.Equal(fx.t, 200, code, out)
	return out
}

func (fx *fixture) lineRefused(id, action string, body map[string]any, reason string) {
	fx.t.Helper()
	_, out := fx.alice.do("POST", "/api/payment/statement-lines/"+id+"/"+action, body)
	assert.Equal(fx.t, reason, out["reason"], "%s: %v", action, out)
}

func (fx *fixture) entryFromLine(id string) map[string]any {
	fx.t.Helper()
	code, out := fx.alice.do("POST", "/api/payment/statement-lines/"+id+"/create-entry", nil)
	require.Equal(fx.t, 200, code, out)
	return out
}
