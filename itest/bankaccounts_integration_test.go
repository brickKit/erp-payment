//go:build integration

package itest

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bankAccounts: the company bank accounts the receipts and payments are registered on; the number is a restricted
// field (docs/design.md, "规则要点"; security 09a).
func (fx *fixture) bankAccounts() {
	t := fx.t
	cny := fx.account(fx.le, "主账户", "6214 8312 3456 7890", "CNY")
	assert.Equal(t, "6214 8312 3456 7890", cny["number"], "Alice's grant lists the number")
	assert.Equal(t, "active", cny["state"])
	assert.Equal(t, float64(1), cny["version"])
	fx.cny = cny["id"].(string)
	fx.usd = fx.account(fx.le, "美元户", "6214 8312 0000 0001", "USD")["id"].(string)
	fx.inactive = fx.account(fx.le, "旧账户", "6214 8312 0000 0099", "CNY")["id"].(string)
	fx.bj = fx.account(fx.le2, "北京主账户", "1100 2200 3300 4400", "CNY")["id"].(string)

	// refused: a legal entity mdm/org does not know, a currency mdm/currency does not, a number taken in the legal
	// entity, a malformed number
	for _, c := range []struct {
		body   map[string]any
		code   int
		reason string
	}{
		{fx.accountBody("00000000-0000-7000-8000-000000000000", "x", "1234 5678", "CNY"), 409, "LEGAL_ENTITY_NOT_USABLE"},
		{fx.accountBody(fx.le, "x", "1234 5679", "XYZ"), 409, "CURRENCY_NOT_USABLE"},
		{fx.accountBody(fx.le, "x", "6214 8312 3456 7890", "CNY"), 409, "CODE_TAKEN"},
		{fx.accountBody(fx.le, "x", "12", "CNY"), 400, "REQUEST_INVALID"},
	} {
		code, out := fx.alice.do("POST", "/api/payment/bank-accounts", c.body)
		assert.Equal(t, c.code, code, out)
		assert.Equal(t, c.reason, out["reason"], out)
	}
	code, out := fx.alice.do("POST", "/api/payment/bank-accounts", fx.accountBody(fx.le2, "x", "6214 8312 3456 7890", "CNY"))
	assert.Equal(t, 200, code, "another legal entity may have the same number: %v", out)

	// the restricted number: Carol's grant does not list it, she sees its last four digits only
	got := fx.as(fx.carol, "/api/payment/bank-accounts/"+fx.cny)
	assert.Equal(t, "****7890", got["number"])
	page := fx.as(fx.carol, "/api/payment/bank-accounts?legal_entity_id="+fx.le+"&currency=USD")
	require.Len(t, page["bank_accounts"], 1, page)
	assert.Equal(t, "****0001", at(page, "bank_accounts", 0, "number"))
	assert.Equal(t, "6214 8312 0000 0001", at(fx.get("/api/payment/bank-accounts?legal_entity_id="+fx.le+"&currency=USD"),
		"bank_accounts", 0, "number"))

	// changes by version; an inactive account takes nothing (the receipts step)
	code, out = fx.alice.do("PATCH", "/api/payment/bank-accounts/"+fx.cny, map[string]any{"name": "基本户", "version": 1})
	require.Equal(t, 200, code, out)
	assert.Equal(t, "基本户", out["name"])
	code, out = fx.alice.do("PATCH", "/api/payment/bank-accounts/"+fx.cny, map[string]any{"name": "又改", "version": 1})
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "VERSION_CONFLICT", out["reason"])
	code, out = fx.alice.do("PATCH", "/api/payment/bank-accounts/"+fx.inactive, map[string]any{"state": "inactive"})
	require.Equal(t, 200, code, out)
	assert.Equal(t, "inactive", out["state"])
	page = fx.get("/api/payment/bank-accounts?legal_entity_id=" + fx.le + "&state=active")
	assert.Len(t, page["bank_accounts"], 2, page)
}

func (fx *fixture) accountBody(le, name, number, currency string) map[string]any {
	return map[string]any{"legal_entity_id": le, "name": name, "bank": "招商银行上海分行", "number": number, "currency": currency}
}

func (fx *fixture) account(le, name, number, currency string) map[string]any {
	fx.t.Helper()
	code, out := fx.alice.do("POST", "/api/payment/bank-accounts", fx.accountBody(le, name, number, currency))
	require.Equal(fx.t, 200, code, out)
	return out
}

// as reads the user surface as c and wants 200.
func (fx *fixture) as(c client, path string) map[string]any {
	fx.t.Helper()
	code, out := c.do("GET", path, nil)
	require.Equal(fx.t, 200, code, out)
	return out
}
