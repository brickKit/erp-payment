// Command seed fills a running erp/payment with the seed data for people (be-assembly-standard's seed and test data
// conventions):
//
//   - access, by the authorization admin: the role maintain (the seed user: every operation, the whole account
//     number); test.cashier.cn, CN01's cashier: registers receipts, releases and executes payment requests, makes
//     entries and reconciles in CN01, reads the whole number; test.finance.lead: approves payment requests and entries
//     in every legal entity, releases and executes them in ES01 and US01 (the seed user applies: an approver or a
//     releaser is never the applicant); the seed user also lists the paused approval tasks;
//   - payment policies: every legal entity's approver is the holders of its position FIN-MGR (mdm/org's seed makes
//     it, mdm/employee's appoints the employee test.finance.lead is linked to);
//   - company bank accounts: CN01 a basic and a general CNY account and a USD one; ES01 an EUR one; US01 a USD one;
//   - receipts against erp/billing's issued invoices in the base currency (paid whole, paid half, left open), one
//     from a payer not known (finance holds it in suspense), one reversed; finance settles them from the fund facts;
//   - payment requests against erp/finance's due payables in every state (paid whole and in part, returned, ready,
//     approved, submitted, draft, rejected with the reason, cancelled);
//   - a bank charge and interest posted; in CN01 a transfer arrived and one in transit, a draft charge;
//   - CN01's basic account's statement: lines matched by their bank references, by hand, a difference resolved, a
//     line left open and one a charge was made from.
//
// The documents are seeded once, into a legal entity whose basic account has no statement line yet (the last step);
// a run that broke off is finished within a day by running it again (the idempotency keys; each document moves on
// from the state it is in). The bank accounts are made when missing, so a second run changes nothing. The legal
// entities come from mdm/org, the invoices from erp/billing, the payables from erp/finance (their seeds run first).
//
//	SEED_IAM_URL (http://localhost:18200)    the identity member's development exchange
//	SEED_AUTHZ_URL (http://localhost:18223)  the authorization member's effect point
//	SEED_ADMIN (test.alice)                  the authorization admin (the install claim)
//	SEED_USER (test.bob), SEED_PASSWORD      the user that writes the data; the test accounts' password
//	ERP_PAYMENT_URL (http://localhost:18228), MDM_ORG_URL (http://localhost:18083), ERP_BILLING_URL (http://localhost:18089)
package main

import (
	"fmt"
	"net/url"
	"os"
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/seed"
)

const (
	prefix  = "「本地测试」"
	cashier = "test.cashier.cn"
	lead    = "test.finance.lead"
)

var entities = []string{"CN01", "ES01", "US01"}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed erp/payment: "+format+"\n", a...)
	os.Exit(1)
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

type client struct{ seed.Client }

func (c client) must(method, path string, body any, header ...string) map[string]any {
	out, err := c.Must(method, path, body, header...)
	check(err)
	return out
}

// granted is must for another account's first writes: while its grant has not reached the component yet the
// request answers 403 or 404 (nothing written), and it is sent again for up to three minutes. An approval waits too:
// a task made before the employee projection was current waits paused until the kernel resolves it again (every
// minute), and the lead is no candidate till then (403).
func (c client) granted(method, path string, body any, header ...string) map[string]any {
	code, out, err := c.UntilGranted(3*time.Minute, method, path, body, header...)
	check(err)
	if code != 200 {
		fail("%s %s: %d %v", method, path, code, out)
	}
	return out
}

// pages reads every page of a list (path carries its query already).
func (c client) pages(path, key string) []map[string]any {
	var out []map[string]any
	cursor := ""
	for {
		page := c.must("GET", path+"&limit=200&cursor="+url.QueryEscape(cursor), nil)
		for _, x := range page[key].([]any) {
			out = append(out, x.(map[string]any))
		}
		if cursor, _ = page["next_cursor"].(string); cursor == "" {
			return out
		}
	}
}

// segment grants an operation on a payment resource in the legal entities (all when none); payment requests carry
// the department too, granted whole; the bank account's restricted number is read where number is set.
func segment(op [2]string, legalEntities []string, number bool) sdkauthz.Segment {
	s := seed.Segment(op[0], op[1])
	le := sdkauthz.Atom{Kind: "all"}
	if len(legalEntities) > 0 {
		le = sdkauthz.Atom{Kind: "in", Values: legalEntities}
	}
	s.Atoms = map[string]sdkauthz.Atom{"legal_entity": le}
	switch op[0] {
	case "erp.payment.request":
		s.Atoms["department"] = sdkauthz.Atom{Kind: "all"}
	case "erp.payment.flow": // the approval tasks' resource has no dimension
		s.Atoms = map[string]sdkauthz.Atom{}
	}
	if number && op[0] == "erp.payment.bank_account" {
		s.ReadFields = []string{"number"}
	}
	return s
}

func segments(les []string, number bool, ops ...[2]string) []sdkauthz.Segment {
	var out []sdkauthz.Segment
	for _, op := range ops {
		out = append(out, segment(op, les, number))
	}
	return out
}

var (
	accRead    = [2]string{"erp.payment.bank_account", "erp.payment.bank_account.read"}
	accManage  = [2]string{"erp.payment.bank_account", "erp.payment.bank_account.manage"}
	recRead    = [2]string{"erp.payment.receipt", "erp.payment.receipt.read"}
	recReg     = [2]string{"erp.payment.receipt", "erp.payment.receipt.register"}
	recRev     = [2]string{"erp.payment.receipt", "erp.payment.receipt.reverse"}
	reqRead    = [2]string{"erp.payment.request", "erp.payment.request.read"}
	reqEdit    = [2]string{"erp.payment.request", "erp.payment.request.edit"}
	reqApprove = [2]string{"erp.payment.request", "erp.payment.request.approve"}
	reqExec    = [2]string{"erp.payment.request", "erp.payment.request.execute"}
	entRead    = [2]string{"erp.payment.entry", "erp.payment.entry.read"}
	entEdit    = [2]string{"erp.payment.entry", "erp.payment.entry.edit"}
	entApprove = [2]string{"erp.payment.entry", "erp.payment.entry.approve"}
	stRead     = [2]string{"erp.payment.statement", "erp.payment.statement.read"}
	stRec      = [2]string{"erp.payment.statement", "erp.payment.statement.reconcile"}
	polRead    = [2]string{"erp.payment.policy", "erp.payment.policy.read"}
	polManage  = [2]string{"erp.payment.policy", "erp.payment.policy.manage"}
	flowDecide = [2]string{"erp.payment.flow", "erp.payment.flow.decide"}
	flowManage = [2]string{"erp.payment.flow", "erp.payment.flow.manage"}
	everyOp    = [][2]string{accRead, accManage, recRead, recReg, recRev, reqRead, reqEdit, reqApprove, reqExec, entRead,
		entEdit, entApprove, stRead, stRec, polRead, polManage, flowDecide, flowManage}
)

// entity is one legal entity and its bank accounts by name.
type entity struct {
	code, id, currency string
	accounts           map[string]string
	approver           string // the position FIN-MGR: its holders approve the legal entity's payments
}

func main() {
	base := env("ERP_PAYMENT_URL", "http://localhost:18228")
	if !seed.Ready(base) {
		fail("erp/payment at %s is not ready", base)
	}
	pw := os.Getenv("SEED_PASSWORD")
	if pw == "" {
		fail("SEED_PASSWORD is not set (the test accounts' password in Casdoor)")
	}
	iam, user, admin := env("SEED_IAM_URL", "http://localhost:18200"), env("SEED_USER", "test.bob"), env("SEED_ADMIN", "test.alice")
	accounts, err := seed.Accounts(iam, pw, admin, user, cashier, lead)
	check(err)
	t := accounts[user].Token
	les := read(t)
	a, err := seed.NewAuthz(env("SEED_AUTHZ_URL", "http://localhost:18223"), accounts[admin].Token, "erp/payment")
	check(err)
	cn := []string{les["CN01"].id}
	_, err = a.GrantRoles([]seed.RoleSpec{
		{Key: "maintain", Name: prefix + "收付款维护", To: []string{user}, Segments: segments(nil, true, everyOp...)},
		{Key: "cashier-cn", Name: prefix + "出纳（CN01）", To: []string{cashier},
			Segments: segments(cn, true, accRead, recRead, recReg, recRev, reqRead, reqExec, entRead, entEdit, stRead, stRec)},
		{Key: "lead", Name: prefix + "财务主管（付款审批）", To: []string{lead},
			Segments: segments(nil, false, accRead, recRead, reqRead, reqApprove, reqExec, entRead, entApprove, stRead, flowDecide)},
	}, accounts)
	check(err)

	c := client{seed.NewClient(base, t)}
	actors := map[string]client{"cashier": {seed.NewClient(base, accounts[cashier].Token)},
		"lead": {seed.NewClient(base, accounts[lead].Token)}}
	for _, code := range entities {
		bankAccounts(c, les[code])
		policy(c, les[code])
	}
	for _, code := range entities {
		e := les[code]
		if done(c, e) {
			fmt.Printf("seed erp/payment: %s's basic account has statement lines already; the documents are not seeded again\n", code)
			continue
		}
		s := &seeder{e: e, c: c, lead: actors["lead"], releaser: actors["lead"]}
		if code == "CN01" {
			s.releaser = actors["cashier"]
		}
		r := s.receipts(t)
		p := s.requests()
		n := s.entries()
		l := s.statement()
		fmt.Printf("seed erp/payment: %s: %d receipts, %d payment requests, %d entries, %d statement lines\n", code, r, p, n, l)
	}
}

// read gathers the legal entities from mdm/org.
func read(t string) map[string]*entity {
	les := map[string]*entity{}
	org := client{seed.NewClient(env("MDM_ORG_URL", "http://localhost:18083"), t)}
	for _, x := range org.must("GET", "/api/org/legal-entities?limit=200", nil)["legal_entities"].([]any) {
		x := x.(map[string]any)
		les[x["code"].(string)] = &entity{code: x["code"].(string), id: x["id"].(string), currency: x["base_currency"].(string),
			accounts: map[string]string{}}
	}
	for _, code := range entities {
		if les[code] == nil {
			fail("mdm/org has no legal entity %s: run its seed first", code)
		}
		for _, p := range org.pages("/api/org/positions?state=active&q=FIN-MGR&legal_entity_id="+les[code].id, "positions") {
			if p["code"] == "FIN-MGR" {
				les[code].approver = p["id"].(string)
			}
		}
		if les[code].approver == "" {
			fail("mdm/org has no %s position FIN-MGR: run its seed first", code)
		}
	}
	return les
}

// policy names the legal entity's approver when it differs: a second run changes nothing.
func policy(c client, e *entity) {
	path := "/api/payment/policies/" + e.id
	code, cur, err := c.UntilGranted(30*time.Second, "GET", path, nil)
	check(err)
	if code != 200 {
		fail("payment policy %s: %d %v", e.code, code, cur)
	}
	if cur["approver_position_id"] == e.approver {
		return
	}
	c.must("PUT", path, map[string]any{"approver_position_id": e.approver, "version": cur["version"]})
}

// demoAccounts are each legal entity's accounts: name, bank, number, currency ("" is the base currency).
var demoAccounts = map[string][][4]string{
	"CN01": {{"基本户", "招商银行上海分行", "6214 8300 0000 0001", ""}, {"一般户", "工商银行上海分行", "6222 0200 0000 0002", ""},
		{"美元户", "中国银行上海分行", "4563 5100 0000 0003", "USD"}},
	"ES01": {{"基本户", "Banco Santander Madrid", "ES91 2100 0418 4502 0005 1332", ""}},
	"US01": {{"基本户", "JPMorgan Chase New York", "0000 1111 2222", ""}},
}

// bankAccounts makes the legal entity's missing accounts (by number).
func bankAccounts(c client, e *entity) {
	have := map[string]string{}
	code, out, err := c.UntilGranted(30e9, "GET", "/api/payment/bank-accounts?limit=200&legal_entity_id="+e.id, nil)
	check(err)
	if code != 200 {
		fail("bank accounts of %s: %d %v", e.code, code, out)
	}
	for _, a := range c.pages("/api/payment/bank-accounts?legal_entity_id="+e.id, "bank_accounts") {
		have[a["number"].(string)] = a["id"].(string)
	}
	made := 0
	for i, a := range demoAccounts[e.code] {
		currency := a[3]
		if currency == "" {
			currency = e.currency
		}
		id := have[a[2]]
		if id == "" {
			id = c.must("POST", "/api/payment/bank-accounts", map[string]any{"legal_entity_id": e.id, "name": a[0], "bank": a[1],
				"number": a[2], "currency": currency}, "Idempotency-Key", fmt.Sprintf("seed-payment-account-%s-%d", e.code, i+1))["id"].(string)
			made++
		}
		e.accounts[a[0]] = id
	}
	fmt.Printf("seed erp/payment: %s: %d bank accounts made\n", e.code, made)
}

// done reports whether the legal entity's documents were seeded: the last step registers its basic account's statement.
func done(c client, e *entity) bool {
	return len(c.must("GET", "/api/payment/statement-lines?limit=1&bank_account_id="+e.accounts["基本户"], nil)["lines"].([]any)) > 0
}
