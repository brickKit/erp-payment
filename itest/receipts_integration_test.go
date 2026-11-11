//go:build integration

package itest

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"strings"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/ids"
	financeevents "github.com/brickKit/erp-finance/contract/v3/events"
	paymentv3 "github.com/brickKit/erp-payment/contract/v3/gen/go/be/erp/payment/v3"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// receipts: money received, registered offline; the fund fact for erp/finance in the same transaction; a reversal
// (docs/design.md, RC01, RC02).
func (fx *fixture) receipts() {
	t := fx.t
	// RC01: registered is received; the fund fact goes out with it (erp/finance's shared contract), and the full state
	key := ids.New().String()
	r := fx.receipt(fx.alice, map[string]any{"bank_account_id": fx.cny, "amount": "1000", "bank_reference": "R-1",
		"payer_id": fx.customer}, key)
	assert.Equal(t, "received", r["state"])
	assert.True(t, strings.HasPrefix(r["number"].(string), "RC-SH-"), r["number"])
	assert.Equal(t, "CNY", r["currency"])
	f := fx.fact(r["fact_id"].(string), core.FundFact)
	fx.valid(core.FundFact, f)
	assert.Equal(t, "receipt", f["kind"])
	assert.Equal(t, "receipt", f["rule_key"])
	assert.Equal(t, fx.customer, f["partner_id"])
	assert.Equal(t, "1000", f["amount"])
	assert.Equal(t, fx.cny, f["company_account_id"])
	assert.Nil(t, f["exchange_rate"], "the base currency has no rate")
	assert.Equal(t, r["id"], at(f, "source", "document_id"))
	assert.Equal(t, r["number"], at(f, "source", "document_number"))
	assert.Equal(t, fx.today(), f["business_date"])
	fx.valid(core.ReceiptChanged, fx.factWhere(core.ReceiptChanged, "id", r["id"].(string)))
	again := fx.receipt(fx.alice, map[string]any{"bank_account_id": fx.cny, "amount": "1000", "bank_reference": "R-1",
		"payer_id": fx.customer}, key)
	assert.Equal(t, r["id"], again["id"], "the same Idempotency-Key answers the same receipt")

	// RC01: refused: the bank reference again on the account, an inactive account, another legal entity's account, a
	// suspended payer, an order that is not the payer's in this legal entity
	order, foreign := ids.New().String(), ids.New().String()
	fx.sales.set(order, fx.le, fx.customer)
	fx.sales.set(foreign, fx.le, fx.supplier)
	for _, c := range []struct {
		body   map[string]any
		reason string
	}{
		{map[string]any{"bank_account_id": fx.cny, "amount": "5", "bank_reference": "R-1"}, "BANK_REFERENCE_TAKEN"},
		{map[string]any{"bank_account_id": fx.inactive, "amount": "5", "bank_reference": "R-2"}, "BANK_ACCOUNT_NOT_USABLE"},
		{map[string]any{"bank_account_id": fx.bj, "amount": "5", "bank_reference": "R-2"}, "BANK_ACCOUNT_NOT_USABLE"},
		{map[string]any{"bank_account_id": fx.cny, "amount": "5", "bank_reference": "R-2", "payer_id": fx.blocked}, "PARTNER_NOT_USABLE"},
		{map[string]any{"bank_account_id": fx.cny, "amount": "5", "bank_reference": "R-2", "payer_id": fx.customer,
			"order_id": foreign}, "ORDER_NOT_USABLE"},
	} {
		code, out := fx.alice.do("POST", "/api/payment/receipts", fx.receiptBody(c.body))
		assert.Equal(t, 409, code, out)
		assert.Equal(t, c.reason, out["reason"], "%v: %v", c.body, out)
	}
	code, out := fx.alice.do("POST", "/api/payment/receipts", fx.receiptBody(map[string]any{"bank_account_id": fx.cny,
		"amount": "0", "bank_reference": "R-2"}))
	assert.Equal(t, 400, code, out)

	// RC01: the order reference goes with the fact; a payer not known is null (finance holds it in suspense); a USD
	// account's receipt carries the rate snapshot
	withOrder := fx.receipt(fx.alice, map[string]any{"bank_account_id": fx.cny, "amount": "300", "bank_reference": "R-3",
		"payer_id": fx.customer, "order_id": order}, "")
	assert.Equal(t, map[string]any{"kind": "sales_order", "id": order}, fx.fact(withOrder["fact_id"].(string), core.FundFact)["order_ref"])
	unknown := fx.receipt(fx.alice, map[string]any{"bank_account_id": fx.cny, "amount": "88", "bank_reference": "R-4"}, "")
	assert.Nil(t, unknown["payer_id"])
	assert.Nil(t, fx.fact(unknown["fact_id"].(string), core.FundFact)["partner_id"])
	usd := fx.receipt(fx.alice, map[string]any{"bank_account_id": fx.usd, "amount": "100", "bank_reference": "U-1",
		"payer_id": fx.customer}, "")
	fu := fx.fact(usd["fact_id"].(string), core.FundFact)
	fx.valid(core.FundFact, fu)
	assert.Equal(t, "USD", fu["currency"])
	assert.Equal(t, map[string]any{"rate": "7.1", "date": fx.today(), "type": "spot"}, fu["exchange_rate"])

	// RC02: a reversal with a reason: the reversing fund fact refers to the first; once reversed, again is ignored;
	// the bank reference is free again
	code, out = fx.bob.do("POST", "/api/payment/receipts/"+r["id"].(string)+"/reverse", map[string]any{})
	assert.Equal(t, 400, code, "a reversal says why: %v", out)
	code, rev := fx.bob.do("POST", "/api/payment/receipts/"+r["id"].(string)+"/reverse", map[string]any{"reason": "银行退票"})
	require.Equal(t, 200, code, rev)
	assert.Equal(t, "reversed", rev["state"])
	assert.Equal(t, "银行退票", rev["reason"])
	fr := fx.factWhere(core.FundFact, "reverses_fact_id", r["fact_id"].(string))
	fx.valid(core.FundFact, fr)
	assert.Equal(t, "reversal", fr["kind"])
	assert.Equal(t, "receipt", fr["rule_key"])
	assert.Equal(t, "1000", fr["amount"])
	code, out = fx.bob.do("POST", "/api/payment/receipts/"+r["id"].(string)+"/reverse", map[string]any{"reason": "再来"})
	require.Equal(t, 200, code, out)
	assert.Equal(t, "银行退票", out["reason"], "a repeat is ignored")
	fx.receipt(fx.alice, map[string]any{"bank_account_id": fx.cny, "amount": "1000", "bank_reference": "R-1",
		"payer_id": fx.customer}, "")

	// the list: by state and by the bank reference
	page := fx.get("/api/payment/receipts?legal_entity_id=" + fx.le + "&state=reversed")
	require.Len(t, page["receipts"], 1, page)
	page = fx.get("/api/payment/receipts?legal_entity_id=" + fx.le + "&q=U-1")
	require.Len(t, page["receipts"], 1, page)
	assert.Equal(t, usd["id"], at(page, "receipts", 0, "id"))
	assert.Len(t, fx.as(fx.carol, "/api/payment/receipts?legal_entity_id="+fx.le)["receipts"], 5, "R-1 twice, R-3, R-4, U-1")

	// FactService.Read: any component reads the facts back by identity; an unknown one has no type
	facts := fx.readFacts(r["fact_id"].(string), fr["fact_id"].(string), ids.New().String())
	assert.Equal(t, core.FundFact, facts[0].GetType())
	assert.Equal(t, core.FundFact, facts[1].GetType())
	assert.Empty(t, facts[2].GetType())
	var back map[string]any
	require.NoError(t, json.Unmarshal(facts[0].GetData(), &back))
	assert.Equal(t, f, back)
}

func (fx *fixture) receiptBody(b map[string]any) map[string]any {
	out := map[string]any{"legal_entity_id": fx.le, "received_on": fx.today()}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// receipt registers a receipt as c (key "" makes a new Idempotency-Key) and wants 200.
func (fx *fixture) receipt(c client, b map[string]any, key string) map[string]any {
	fx.t.Helper()
	var keys []string
	if key != "" {
		keys = []string{key}
	}
	code, out := c.do("POST", "/api/payment/receipts", fx.receiptBody(b), keys...)
	require.Equal(fx.t, 200, code, out)
	return out
}

// readFacts calls FactService.Read as erp/finance.
func (fx *fixture) readFacts(ids ...string) []*paymentv3.Fact {
	fx.t.Helper()
	conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(fx.t, core.Finance).ClientConfig(core.ID))))
	require.NoError(fx.t, err)
	defer conn.Close()
	r, err := paymentv3.NewFactServiceClient(conn).Read(fx.ctx, &paymentv3.ReadRequest{FactIds: ids})
	require.NoError(fx.t, err)
	return r.GetFacts()
}

// valid checks an event's payload against its schema in contract/events; the fund and accounting facts' schemas
// refer to erp/finance's shared contracts, loaded from its contract module (nothing copied).
func (fx *fixture) valid(typ string, payload map[string]any) {
	fx.t.Helper()
	c := jsonschema.NewCompiler()
	require.NoError(fx.t, fs.WalkDir(financeevents.Shared, "shared", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(financeevents.Shared, p)
		if err != nil {
			return err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		return c.AddResource("https://brickkit.dev/erp-finance/events/"+p, doc)
	}))
	s, err := c.Compile("../contract/events/" + typ + ".schema.json")
	require.NoError(fx.t, err)
	raw, _ := json.Marshal(payload)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(fx.t, err)
	assert.NoError(fx.t, s.Validate(doc), "%s: %s", typ, raw)
}
