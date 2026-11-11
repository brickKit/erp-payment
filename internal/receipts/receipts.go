package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	bankaccounts "github.com/brickKit/erp-payment/v3/internal/bankaccounts"
	core "github.com/brickKit/erp-payment/v3/internal/core"
	facts "github.com/brickKit/erp-payment/v3/internal/facts"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"
	matching "github.com/brickKit/erp-payment/v3/internal/matching"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/list"
	"github.com/brickKit/be-sdk-go/v3/search"
	"github.com/jackc/pgx/v5"
)

// Receipt is a receipt as the user surface and erp.receipt.changed.v1 carry it.
type Receipt struct {
	ID            string  `json:"id"`
	Number        string  `json:"number"`
	LegalEntityID string  `json:"legal_entity_id"`
	BankAccountID string  `json:"bank_account_id"`
	PayerID       *string `json:"payer_id"`
	Currency      string  `json:"currency"`
	Amount        string  `json:"amount"`
	ReceivedOn    string  `json:"received_on"`
	BankReference string  `json:"bank_reference"`
	OrderID       *string `json:"order_id"`
	Description   *string `json:"description"`
	State         string  `json:"state"`
	FactID        string  `json:"fact_id"`
	MakerID       string  `json:"maker_id"`
	Reason        *string `json:"reason"`
	Version       int64   `json:"version"`
	UpdatedAt     string  `json:"updated_at"`
	createdAt     string
}

const cols = `r.id::text, r.number, r.legal_entity_id::text, r.bank_account_id::text, r.payer_id::text, r.currency,
	r.amount::text, r.received_on::text, r.bank_reference, r.order_id::text, r.description, r.state, r.fact_id::text,
	r.maker_id, r.reason, r.version, r.updated_at, r.created_at`

func scan(row pgx.Row) (Receipt, error) {
	var r Receipt
	var at, created time.Time
	err := row.Scan(&r.ID, &r.Number, &r.LegalEntityID, &r.BankAccountID, &r.PayerID, &r.Currency, &r.Amount, &r.ReceivedOn,
		&r.BankReference, &r.OrderID, &r.Description, &r.State, &r.FactID, &r.MakerID, &r.Reason, &r.Version, &at, &created)
	r.UpdatedAt, r.createdAt = canon.Time(at), canon.Time(created)
	return r, err
}

// Get reads a receipt (FOR UPDATE when lock).
func Get(ctx context.Context, tx db.Tx, id string, lock bool) (Receipt, error) {
	q := `SELECT ` + cols + ` FROM receipt r WHERE r.id = $1::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	r, err := scan(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, core.NotFound("receipt")
	}
	return r, err
}

type receiptIn struct {
	LegalEntityID string  `json:"legal_entity_id"`
	BankAccountID string  `json:"bank_account_id"`
	Amount        string  `json:"amount"`
	ReceivedOn    string  `json:"received_on"`
	BankReference string  `json:"bank_reference"`
	PayerID       *string `json:"payer_id"`
	OrderID       *string `json:"order_id"`
	Description   *string `json:"description"`
}

func (in receiptIn) check() error {
	switch {
	case !core.UUIDForm.MatchString(in.LegalEntityID):
		return core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	case !core.UUIDForm.MatchString(in.BankAccountID):
		return core.Invalid("bank_account_id", "bank_account_id is a bank account's ID")
	case !core.DateForm.MatchString(in.ReceivedOn):
		return core.Invalid("received_on", "received_on is YYYY-MM-DD")
	case strings.TrimSpace(in.BankReference) == "":
		return core.Invalid("bank_reference", "bank_reference is required")
	case in.PayerID != nil && !core.UUIDForm.MatchString(*in.PayerID):
		return core.Invalid("payer_id", "payer_id is a partner's ID")
	case in.OrderID != nil && !core.UUIDForm.MatchString(*in.OrderID):
		return core.Invalid("order_id", "order_id is a sales order's ID")
	case in.OrderID != nil && in.PayerID == nil:
		return core.Invalid("order_id", "an order reference needs its payer")
	}
	return nil
}

// evidence is what a registration takes from the other components before its transaction.
type evidence struct {
	le      masterdata.LegalEntity
	account bankaccounts.Account
	amount  string
	rate    *masterdata.Rate
}

func gather(m *core.Base, ctx context.Context, in receiptIn) (evidence, error) {
	var ev evidence
	var err error
	if ev.le, err = masterdata.LegalEntityOf(m, ctx, in.LegalEntityID); err != nil {
		return ev, err
	}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		ev.account, err = bankaccounts.Usable(ctx, tx, in.BankAccountID, in.LegalEntityID)
		return err
	})
	if err != nil {
		return ev, err
	}
	if in.PayerID != nil {
		if _, err := masterdata.PartnerOf(m, ctx, *in.PayerID); err != nil {
			return ev, err
		}
	}
	if in.OrderID != nil {
		o, err := masterdata.OrderOf(m, ctx, *in.OrderID)
		if err != nil {
			return ev, err
		}
		if o.LegalEntityID != in.LegalEntityID || o.CustomerID != *in.PayerID {
			return ev, core.ErrOrder.New("order " + *in.OrderID + " is not the payer's in this legal entity")
		}
	}
	scale, err := masterdata.CurrencyScale(m, ctx, ev.account.Currency)
	if err != nil {
		return ev, err
	}
	if ev.amount, err = core.Decimal("amount", in.Amount, scale, true); err != nil {
		return ev, err
	}
	ev.rate, err = masterdata.RateOf(m, ctx, ev.account.Currency, ev.le.BaseCurrency, in.ReceivedOn)
	return ev, err
}

// RegisterReceipt is RC01: registered is received; the fund fact goes out in the same transaction.
func RegisterReceipt(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	var in receiptIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if err := in.check(); err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpReceiptRegister, core.EntityFacts(in.LegalEntityID))
	if err != nil {
		return err
	}
	ev, err := gather(m, ctx, in)
	if err != nil {
		return err
	}
	// a gapped series takes its number in its own transaction, before the receipt's (a replay leaves a gap)
	number, err := core.Number(m, ctx, "RC", in.LegalEntityID, ev.le.Code, in.ReceivedOn)
	if err != nil {
		return err
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		a, err := bankaccounts.Usable(ctx, tx, in.BankAccountID, in.LegalEntityID)
		if err != nil {
			return idem.Outcome{}, err
		}
		if a.Currency != ev.account.Currency {
			return idem.Outcome{}, core.ErrBankAccount.New("the bank account's currency changed")
		}
		id, factID := ids.New().String(), ids.New().String()
		tag, err := tx.Exec(ctx, `INSERT INTO receipt (id, number, legal_entity_id, bank_account_id, payer_id, currency,
			amount, received_on, bank_reference, order_id, description, fact_id, maker_id, search, search_version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			ON CONFLICT (bank_account_id, bank_reference) WHERE state = 'received' DO NOTHING`,
			id, number, in.LegalEntityID, in.BankAccountID, in.PayerID, a.Currency, ev.amount, in.ReceivedOn,
			in.BankReference, in.OrderID, in.Description, factID, actor, search.Text(number, in.BankReference), search.Version)
		if err != nil {
			return idem.Outcome{}, err
		}
		if tag.RowsAffected() == 0 {
			return idem.Outcome{}, core.ErrBankReference.New("bank reference " + in.BankReference + " is registered on this account")
		}
		f := facts.Fund{FactID: factID, Source: facts.Source{Component: core.ID, DocumentType: "receipt", DocumentID: id,
			DocumentNumber: &number}, LegalEntityID: in.LegalEntityID, BusinessDate: in.ReceivedOn, PostingDate: in.ReceivedOn,
			RuleKey: "receipt", Currency: a.Currency, ExchangeRate: ev.rate, RecordedAt: facts.Now(), Kind: "receipt",
			PartnerID: in.PayerID, CompanyAccountID: a.ID, Amount: ev.amount}
		if in.OrderID != nil {
			f.OrderRef = &facts.OrderRef{Kind: "sales_order", ID: *in.OrderID}
		}
		if err := f.Publish(m, ctx, tx); err != nil {
			return idem.Outcome{}, err
		}
		if err := changed(m, ctx, tx, actor, core.OpReceiptRegister, id); err != nil {
			return idem.Outcome{}, err
		}
		return core.IDResult(id), matching.Document(ctx, tx, a.ID, &in.BankReference) // a line waiting for it (BR01)
	})
	if err != nil {
		return err
	}
	return answer(m, c, ctx, core.ResultID(out))
}

// changed publishes the receipt's full state and audits it, in the transaction that changed it.
func changed(m *core.Base, ctx context.Context, tx db.Tx, actor, op, id string) error {
	r, err := Get(ctx, tx, id, false)
	if err != nil {
		return err
	}
	if err := core.Emit(m, ctx, tx, core.ReceiptChanged, id, r.Version, r.LegalEntityID, r); err != nil {
		return err
	}
	return core.Change(m, ctx, tx, actor, op, core.Receipts, id, r)
}

func answer(m *core.Base, c *httpx.Context, ctx context.Context, id string) error {
	var r Receipt
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		r, err = Get(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, r)
}

func GetReceipt(m *core.Base, c *httpx.Context) error {
	id := c.Param("id")
	if !core.UUIDForm.MatchString(id) {
		return core.NotFound("receipt")
	}
	var r Receipt
	err := m.RT.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		var err error
		r, err = Get(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, core.OpReceiptRead, core.EntityFacts(r.LegalEntityID)); err != nil {
		return err
	}
	return c.JSON(200, r)
}

// ReverseReceipt is RC02: a reason; the reversing fund fact refers to the receipt's, in the same transaction.
func ReverseReceipt(m *core.Base, c *httpx.Context) error {
	var in struct {
		Reason  *string `json:"reason"`
		Version *int64  `json:"version"`
	}
	if err := core.BindOptional(c, &in); err != nil {
		return err
	}
	if in.Reason == nil || strings.TrimSpace(*in.Reason) == "" {
		return core.Invalid("reason", "a reversal says why")
	}
	id := c.Param("id")
	if !core.UUIDForm.MatchString(id) {
		return core.NotFound("receipt")
	}
	var cur Receipt
	err := m.RT.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		var err error
		cur, err = Get(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpReceiptReverse, core.EntityFacts(cur.LegalEntityID))
	if err != nil {
		return err
	}
	le, err := masterdata.LegalEntityOf(m, ctx, cur.LegalEntityID)
	if err != nil {
		return err
	}
	today := todayIn(le.Timezone)
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		r, err := Get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		d := Lifecycle.Decide(r.State, Reverse)
		switch d.Effect {
		case core.Ignore:
			return nil
		case core.Refuse:
			return core.Refused(d, r.State)
		}
		if in.Version != nil && *in.Version != r.Version {
			return core.ErrVersion.New(fmt.Sprintf("the receipt is at version %d", r.Version))
		}
		var f facts.Fund
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT data::text FROM payment_fact WHERE fact_id = $1`, r.FactID).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		reversal := ids.New().String()
		original := f.FactID
		f.FactID, f.Kind, f.ReversesFactID, f.RecordedAt = reversal, "reversal", &original, facts.Now()
		f.BusinessDate, f.PostingDate = today, today
		if err := f.Publish(m, ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE receipt SET state = $2, reason = $3, reversal_fact_id = $4, version = version + 1,
			updated_at = now() WHERE id = $1`, id, Reversed, *in.Reason, reversal); err != nil {
			return err
		}
		return changed(m, ctx, tx, actor, core.OpReceiptReverse, id)
	})
	if err != nil {
		return err
	}
	return answer(m, c, ctx, id)
}

// todayIn is today's date in the legal entity's time zone (a reversal's business date).
func todayIn(zone string) string {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc).Format("2006-01-02")
}

var receiptList = list.Spec{Operation: core.OpReceiptRead, Alias: "r", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "created_at", Column: "created_at", Kind: list.Time, Sortable: true},
		{Field: "legal_entity_id", Column: "legal_entity_id", Kind: list.UUID, Filterable: true},
		{Field: "bank_account_id", Column: "bank_account_id", Kind: list.UUID, Filterable: true},
		{Field: "payer_id", Column: "payer_id", Kind: list.UUID, Filterable: true},
		{Field: "state", Column: "state", Kind: list.Text, Filterable: true},
		{Field: "received_on", Column: "received_on", Kind: list.Date, Filterable: true}},
	Search: []list.SearchColumn{{Name: "number_reference", Column: "search", Fields: []string{"number", "bank_reference"}}}}

// ListReceipts is one page, newest first.
func ListReceipts(m *core.Base, c *httpx.Context) error {
	var f []list.Filter
	for _, k := range []string{"legal_entity_id", "bank_account_id", "payer_id", "state"} {
		if v := c.Query(k); v != "" {
			f = append(f, list.Filter{Field: k, Op: "eq", Values: []string{v}})
		}
	}
	if v := c.Query("from"); v != "" {
		f = append(f, list.Filter{Field: "received_on", Op: "gte", Values: []string{v}})
	}
	if v := c.Query("to"); v != "" {
		f = append(f, list.Filter{Field: "received_on", Op: "lte", Values: []string{v}})
	}
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	s, _ := sdkauthz.From(c.Ctx())
	ctx, q, err := list.Build(c.Ctx(), s, receiptList, list.Request{Filters: f, Q: c.Query("q"), Limit: limit,
		Cursor: c.Query("cursor"), Sort: []list.Order{{Field: "created_at", Desc: true}}})
	if err != nil {
		return err
	}
	out, next, err := list.Page(ctx, m.RT.DB, q, "receipt", cols, scan,
		func(r Receipt) ([]string, string) { return []string{r.createdAt}, r.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"receipts": out, "next_cursor": next})
}
