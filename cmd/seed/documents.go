package main

import (
	"fmt"
	"math/big"
	"time"

	"github.com/brickKit/be-sdk-go/v3/seed"
)

// seeder seeds one legal entity's documents: the seed user applies and makes, lead approves, releaser releases and
// executes (never the applicant).
type seeder struct {
	e                       *entity
	c, lead, releaser       client
	paidRefs                []paidRef // the money that went out, for the statement
	charge, interest, trans string
}

type paidRef struct{ ref, amount string }

func (s *seeder) key(kind string, n int) string {
	return fmt.Sprintf("seed-payment-%s-%s-%d", kind, s.e.code, n)
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

// half is half of a decimal amount, rounded down to two places.
func half(v string) string {
	r, _ := new(big.Rat).SetString(v)
	r.Quo(r, big.NewRat(2, 1))
	f, _ := new(big.Rat).SetString(r.FloatString(3))
	cents := new(big.Int).Quo(new(big.Int).Mul(f.Num(), big.NewInt(100)), f.Denom())
	return new(big.Rat).SetFrac(cents, big.NewInt(100)).FloatString(2)
}

// receipts registers money received against erp/billing's issued invoices in the base currency (every third paid
// whole, every third half, the rest left open), one from a payer not known, and one reversed.
func (s *seeder) receipts(token string) int {
	billing := client{seed.NewClient(env("ERP_BILLING_URL", "http://localhost:18089"), token)}
	acc := s.e.accounts["基本户"]
	n := 0
	register := func(body map[string]any) map[string]any {
		body["legal_entity_id"], body["bank_account_id"], body["received_on"] = s.e.id, acc, today()
		n++
		r := s.c.must("POST", "/api/payment/receipts", body, "Idempotency-Key", s.key("receipt", n))
		s.paidRefs = append(s.paidRefs, paidRef{body["bank_reference"].(string), r["amount"].(string)})
		return r
	}
	for i, inv := range billing.pages("/api/billing/invoices?state=issued&legal_entity_id="+s.e.id, "invoices") {
		if inv["currency"] != s.e.currency || i%3 == 2 {
			continue
		}
		gross := inv["totals"].(map[string]any)["gross"].(string)
		if i%3 == 1 {
			gross = half(gross)
		}
		register(map[string]any{"amount": gross, "bank_reference": "RC-" + inv["number"].(string),
			"payer_id": inv["customer"].(map[string]any)["partner_id"], "description": prefix + "货款 " + inv["number"].(string)})
	}
	register(map[string]any{"amount": "880.00", "bank_reference": "UNKNOWN-" + s.e.code, "description": prefix + "付款方待查明"})
	wrong := register(map[string]any{"amount": "123.45", "bank_reference": "WRONG-" + s.e.code, "description": prefix + "登记错了"})
	s.paidRefs = s.paidRefs[:len(s.paidRefs)-1]
	if s.c.must("GET", "/api/payment/receipts/"+wrong["id"].(string), nil)["state"] == "received" {
		s.c.must("POST", "/api/payment/receipts/"+wrong["id"].(string)+"/reverse", map[string]any{"reason": prefix + "金额登记错误，冲正后重登"})
	}
	return n
}

// requests makes a payment request for each of erp/finance's due payables, one state each in turn.
func (s *seeder) requests() int {
	wants := []string{"paid", "partial", "ready", "approved", "submitted", "draft", "rejected", "cancelled", "returned"}
	items := s.c.pages("/api/payment/proposals?kind=payable&due_on_or_before=9999-12-31&legal_entity_id="+s.e.id, "items")
	n := 0
	for i, it := range items {
		if it["currency"] != s.e.currency {
			continue
		}
		want := wants[n%len(wants)]
		body := map[string]any{"legal_entity_id": s.e.id, "kind": "payment", "payee_id": it["partner_id"],
			"bank_account_id": s.e.accounts["基本户"], "purpose": prefix + "货款", "reference": it["source_number"],
			"lines": []any{map[string]any{"open_item_id": it["open_item_id"], "amount": it["available"]}}}
		r := s.c.must("POST", "/api/payment/requests", body, "Idempotency-Key", s.key("request", i))
		s.request(r["id"].(string), it["available"].(string), want, n)
		n++
	}
	return n
}

// request moves request id on to want from the state it is in.
func (s *seeder) request(id, amount, want string, n int) {
	path := "/api/payment/requests/" + id
	state := func() string { return s.c.must("GET", path, nil)["state"].(string) }
	act := func(c client, action string, body any) {
		if body == nil {
			body = map[string]any{}
		}
		c.must("POST", path+"/"+action, body)
	}
	if want == "draft" {
		return
	}
	if state() == "draft" && want != "rejected" || state() == "draft" && s.c.must("GET", path, nil)["reason"] == nil {
		act(s.c, "submit", nil)
	}
	switch want {
	case "submitted":
		return
	case "rejected":
		if state() == "submitted" {
			act(s.lead, "reject", map[string]any{"reason": prefix + "请附上供应商的对账单"})
		}
		return
	case "cancelled":
		if state() == "submitted" {
			act(s.c, "cancel", nil)
		}
		return
	}
	if state() == "submitted" {
		act(s.lead, "approve", nil)
	}
	if want == "approved" {
		return
	}
	if state() == "approved" {
		act(s.releaser, "release", nil)
	}
	if want == "ready" {
		return
	}
	paid := amount
	if want == "partial" {
		paid = half(amount)
	}
	ref := fmt.Sprintf("PAY-%s-%03d", s.e.code, n+1)
	if state() == "ready" {
		act(s.releaser, "execute", map[string]any{"paid_amount": paid, "paid_on": today(), "bank_reference": ref})
		s.paidRefs = append(s.paidRefs, paidRef{ref, "-" + paid})
	}
	if want == "returned" && state() == "paid" {
		act(s.releaser, "return", map[string]any{"reason": prefix + "收款账户名称不符，银行退回"})
	}
}

// entries posts a bank charge and interest; CN01 a transfer arrived, one in transit and a draft charge.
func (s *seeder) entries() int {
	acc := s.e.accounts["基本户"]
	n := 0
	entry := func(kind, to, amount, what, want string) string {
		body := map[string]any{"legal_entity_id": s.e.id, "kind": kind, "bank_account_id": acc, "amount": amount,
			"entry_date": today(), "description": prefix + what}
		if to != "" {
			body["to_bank_account_id"] = to
		}
		n++
		e := s.c.must("POST", "/api/payment/entries", body, "Idempotency-Key", s.key("entry", n))
		path := "/api/payment/entries/" + e["id"].(string)
		state := func() string { return s.c.must("GET", path, nil)["state"].(string) }
		if want != "draft" && state() == "draft" {
			s.c.must("POST", path+"/submit", map[string]any{})
		}
		if want != "draft" && state() == "submitted" {
			s.lead.must("POST", path+"/approve", map[string]any{})
		}
		if want == "posted" && state() == "in_transit" {
			s.c.must("POST", path+"/arrive", map[string]any{"arrived_on": today()})
		}
		return e["id"].(string)
	}
	s.charge = entry("charge", "", "35.00", "银行手续费", "posted")
	s.interest = entry("interest", "", "128.50", "活期利息", "posted")
	if s.e.code == "CN01" {
		s.trans = entry("transfer", s.e.accounts["一般户"], "50000.00", "基本户调一般户", "posted")
		entry("transfer", s.e.accounts["一般户"], "20000.00", "基本户调一般户（在途）", "in_transit")
		entry("charge", "", "6.00", "网银年费", "draft")
	}
	return n
}

// statement registers the basic account's statement: the receipts' and payments' lines match by their references,
// the charge, the interest and the transfer by hand; a difference resolved, one line left open, one a charge was
// made from.
func (s *seeder) statement() int {
	acc := s.e.accounts["基本户"]
	var lines []any
	for _, p := range s.paidRefs {
		lines = append(lines, map[string]any{"booked_on": today(), "amount": p.amount, "bank_reference": p.ref,
			"text": prefix + p.ref})
	}
	extra := []struct{ amount, text string }{{"-35.00", "手续费"}, {"128.50", "利息"}, {"66.60", "银行误入"},
		{"-12.00", "短信服务费"}, {"-3.30", "待查"}}
	if s.e.code == "CN01" {
		extra = append(extra, struct{ amount, text string }{"-50000.00", "调拨转出"})
	}
	for _, x := range extra {
		lines = append(lines, map[string]any{"booked_on": today(), "amount": x.amount, "text": prefix + x.text})
	}
	out := s.c.must("POST", "/api/payment/statement-lines", map[string]any{"bank_account_id": acc, "lines": lines},
		"Idempotency-Key", s.key("statement", 1))
	byText := map[string]string{}
	for _, l := range out["lines"].([]any) {
		l := l.(map[string]any)
		if t, ok := l["text"].(string); ok {
			byText[t] = l["id"].(string)
		}
	}
	match := func(text, typ, id string) {
		s.c.must("POST", "/api/payment/statement-lines/"+byText[prefix+text]+"/match", map[string]any{"document_type": typ, "document_id": id})
	}
	match("手续费", "bank_entry", s.charge)
	match("利息", "bank_entry", s.interest)
	if s.trans != "" {
		match("调拨转出", "bank_entry", s.trans)
	}
	s.c.must("POST", "/api/payment/statement-lines/"+byText[prefix+"银行误入"]+"/resolve",
		map[string]any{"reason": prefix + "银行误入他人款项，已联系银行冲回"})
	s.c.must("POST", "/api/payment/statement-lines/"+byText[prefix+"短信服务费"]+"/create-entry", nil,
		"Idempotency-Key", s.key("line-entry", 1))
	return len(lines)
}
