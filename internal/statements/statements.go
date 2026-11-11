package statements

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	bankaccounts "github.com/brickKit/erp-payment/v3/internal/bankaccounts"
	core "github.com/brickKit/erp-payment/v3/internal/core"
	entries "github.com/brickKit/erp-payment/v3/internal/entries"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"
	matching "github.com/brickKit/erp-payment/v3/internal/matching"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/list"
	"github.com/brickKit/be-sdk-go/v3/money"
	"github.com/jackc/pgx/v5"
)

// Document is the document a line matched.
type Document struct {
	DocumentType string `json:"document_type"`
	DocumentID   string `json:"document_id"`
	Number       string `json:"number"`
}

// Line is a statement line as the user surface reads it.
type Line struct {
	ID            string    `json:"id"`
	BankAccountID string    `json:"bank_account_id"`
	BookedOn      string    `json:"booked_on"`
	Amount        string    `json:"amount"`
	BankReference *string   `json:"bank_reference"`
	Text          *string   `json:"text"`
	State         string    `json:"state"`
	Matched       *Document `json:"matched"`
	Note          *string   `json:"note"`
	Version       int64     `json:"version"`
	UpdatedAt     string    `json:"updated_at"`
	legalEntityID string
}

const cols = `l.id::text, l.bank_account_id::text, l.booked_on::text, l.amount::text, l.bank_reference, l.text, l.state,
	l.matched_type, l.matched_id::text, l.matched_number, l.note, l.version, l.updated_at, l.legal_entity_id::text`

func scan(row pgx.Row) (Line, error) {
	var l Line
	var at time.Time
	var mt, mid, mn *string
	err := row.Scan(&l.ID, &l.BankAccountID, &l.BookedOn, &l.Amount, &l.BankReference, &l.Text, &l.State, &mt, &mid, &mn,
		&l.Note, &l.Version, &at, &l.legalEntityID)
	l.UpdatedAt = canon.Time(at)
	if mt != nil && l.State == Matched {
		l.Matched = &Document{DocumentType: *mt, DocumentID: *mid, Number: *mn}
	}
	return l, err
}

func get(ctx context.Context, tx db.Tx, id string, lock bool) (Line, error) {
	q := `SELECT ` + cols + ` FROM statement_line l WHERE l.id = $1::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	l, err := scan(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return l, core.NotFound("statement line")
	}
	return l, err
}

func read(m *core.Base, ctx context.Context, id string) (Line, error) {
	var l Line
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		l, err = get(ctx, tx, id, false)
		return err
	})
	return l, err
}

// account is the bank account a call names, with the operation decided on its legal entity.
func account(m *core.Base, c *httpx.Context, id, op string) (context.Context, string, bankaccounts.Account, error) {
	var a bankaccounts.Account
	if !core.UUIDForm.MatchString(id) {
		return nil, "", a, core.NotFound("bank account")
	}
	err := m.RT.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		var err error
		a, err = bankaccounts.Get(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return nil, "", a, err
	}
	ctx, actor, err := core.Check(c, op, core.EntityFacts(a.LegalEntityID))
	return ctx, actor, a, err
}

type lineIn struct {
	BookedOn      string  `json:"booked_on"`
	Amount        string  `json:"amount"`
	BankReference *string `json:"bank_reference"`
	Text          *string `json:"text"`
}

// signed is a non-zero decimal of at most scale places, money in positive, out negative.
func signed(field, v string, scale int32) (string, error) {
	neg := strings.HasPrefix(v, "-")
	d, err := core.Decimal(field, strings.TrimPrefix(v, "-"), scale, true)
	if err != nil {
		return "", err
	}
	if neg {
		return "-" + d, nil
	}
	return d, nil
}

type registered struct {
	IDs     []string `json:"ids"`
	Skipped []string `json:"skipped"`
}

// RegisterLines is BR01: a statement's lines of one account; a bank reference registered on the account already is
// skipped; each new line matched once by its reference.
func RegisterLines(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	var in struct {
		BankAccountID string   `json:"bank_account_id"`
		Lines         []lineIn `json:"lines"`
	}
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if len(in.Lines) == 0 || len(in.Lines) > core.MaxStatementLines {
		return core.Invalid("lines", fmt.Sprintf("1 to %d lines", core.MaxStatementLines))
	}
	ctx, actor, a, err := account(m, c, in.BankAccountID, core.OpReconcile)
	if err != nil {
		return err
	}
	scale, err := masterdata.CurrencyScale(m, ctx, a.Currency)
	if err != nil {
		return err
	}
	amounts := make([]string, len(in.Lines))
	for i, l := range in.Lines {
		if !core.DateForm.MatchString(l.BookedOn) {
			return core.Invalid(fmt.Sprintf("lines[%d].booked_on", i), "booked_on is YYYY-MM-DD")
		}
		if amounts[i], err = signed(fmt.Sprintf("lines[%d].amount", i), l.Amount, scale); err != nil {
			return err
		}
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		r := registered{IDs: []string{}, Skipped: []string{}}
		for i, l := range in.Lines {
			id := ids.New().String()
			tag, err := tx.Exec(ctx, `INSERT INTO statement_line (id, legal_entity_id, bank_account_id, booked_on, amount,
				bank_reference, text) VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (bank_account_id, bank_reference) WHERE bank_reference IS NOT NULL DO NOTHING`,
				id, a.LegalEntityID, a.ID, l.BookedOn, amounts[i], l.BankReference, l.Text)
			if err != nil {
				return idem.Outcome{}, err
			}
			if tag.RowsAffected() == 0 {
				r.Skipped = append(r.Skipped, *l.BankReference)
				continue
			}
			if _, err := matching.ByReference(ctx, tx, id); err != nil {
				return idem.Outcome{}, err
			}
			line, err := get(ctx, tx, id, false)
			if err != nil {
				return idem.Outcome{}, err
			}
			if err := core.Change(m, ctx, tx, actor, core.OpReconcile, core.Statements, id, line); err != nil {
				return idem.Outcome{}, err
			}
			r.IDs = append(r.IDs, id)
		}
		b, err := json.Marshal(r)
		return idem.Outcome{Status: 200, Result: b}, err
	})
	if err != nil {
		return err
	}
	var r registered
	if err := json.Unmarshal(out.Result, &r); err != nil {
		return err
	}
	lines := []Line{}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		for _, id := range r.IDs {
			l, err := get(ctx, tx, id, false)
			if err != nil {
				return err
			}
			lines = append(lines, l)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"lines": lines, "skipped": r.Skipped})
}

var lineList = list.Spec{Operation: core.OpStatementRead, Alias: "l", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "booked_on", Column: "booked_on", Kind: list.Date, Sortable: true, Filterable: true},
		{Field: "bank_account_id", Column: "bank_account_id", Kind: list.UUID, Filterable: true},
		{Field: "state", Column: "state", Kind: list.Text, Filterable: true}}}

// ListLines is one page of an account's lines by date.
func ListLines(m *core.Base, c *httpx.Context) error {
	acc := c.Query("bank_account_id")
	if !core.UUIDForm.MatchString(acc) {
		return core.Invalid("bank_account_id", "bank_account_id is a bank account's ID")
	}
	f := []list.Filter{{Field: "bank_account_id", Op: "eq", Values: []string{acc}}}
	if v := c.Query("state"); v != "" {
		f = append(f, list.Filter{Field: "state", Op: "eq", Values: []string{v}})
	}
	if v := c.Query("from"); v != "" {
		f = append(f, list.Filter{Field: "booked_on", Op: "gte", Values: []string{v}})
	}
	if v := c.Query("to"); v != "" {
		f = append(f, list.Filter{Field: "booked_on", Op: "lte", Values: []string{v}})
	}
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	s, _ := sdkauthz.From(c.Ctx())
	ctx, q, err := list.Build(c.Ctx(), s, lineList, list.Request{Filters: f, Limit: limit, Cursor: c.Query("cursor"),
		Sort: []list.Order{{Field: "booked_on"}}})
	if err != nil {
		return err
	}
	out, next, err := list.Page(ctx, m.RT.DB, q, "statement_line", cols, scan,
		func(l Line) ([]string, string) { return []string{l.BookedOn}, l.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"lines": out, "next_cursor": next})
}

// Action is match, unmatch, resolve and reopen (BR02, BR03), as the table decides them in the line's state.
func Action(m *core.Base, command string) httpx.Handler {
	return func(c *httpx.Context) error {
		var in struct {
			DocumentType string  `json:"document_type"`
			DocumentID   string  `json:"document_id"`
			Reason       *string `json:"reason"`
			Version      *int64  `json:"version"`
		}
		if err := core.BindOptional(c, &in); err != nil {
			return err
		}
		if command == Resolve && (in.Reason == nil || strings.TrimSpace(*in.Reason) == "") {
			return core.Invalid("reason", "how the difference was handled")
		}
		if command == Match && (!core.UUIDForm.MatchString(in.DocumentID) || (in.DocumentType != "receipt" &&
			in.DocumentType != "payment_request" && in.DocumentType != "bank_entry")) {
			return core.Invalid("document_id", "a receipt's, a payment request's or a bank entry's ID")
		}
		cur, err := read(m, c.Ctx(), c.Param("id"))
		if err != nil {
			return err
		}
		ctx, actor, err := core.Check(c, core.OpReconcile, core.EntityFacts(cur.legalEntityID))
		if err != nil {
			return err
		}
		var out Line
		err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
			l, err := get(ctx, tx, cur.ID, true)
			if err != nil {
				return err
			}
			out = l
			d := Lifecycle.Decide(l.State, command)
			switch d.Effect {
			case core.Ignore:
				return nil
			case core.Refuse:
				return core.Refused(d, l.State)
			}
			if in.Version != nil && *in.Version != l.Version {
				return core.ErrVersion.New(fmt.Sprintf("the line is at version %d", l.Version))
			}
			var err2 error
			switch command {
			case Match:
				err2 = matching.Match(ctx, tx, l.ID, in.DocumentType, in.DocumentID)
			case Unmatch:
				_, err2 = tx.Exec(ctx, `UPDATE statement_line SET state = 'unmatched', matched_type = NULL, matched_id = NULL,
					matched_number = NULL, matched_leg = '', version = version + 1, updated_at = now() WHERE id = $1`, l.ID)
			case Resolve:
				_, err2 = tx.Exec(ctx, `UPDATE statement_line SET state = 'resolved', note = $2, version = version + 1,
					updated_at = now() WHERE id = $1`, l.ID, *in.Reason)
			case Reopen:
				_, err2 = tx.Exec(ctx, `UPDATE statement_line SET state = 'unmatched', version = version + 1, updated_at = now()
					WHERE id = $1`, l.ID)
			}
			if err2 != nil {
				return err2
			}
			if out, err = get(ctx, tx, l.ID, false); err != nil {
				return err
			}
			return core.Change(m, ctx, tx, actor, core.OpReconcile, core.Statements, l.ID, out)
		})
		if err != nil {
			return err
		}
		return c.JSON(200, out)
	}
}

// CreateEntryFromLine is BR04: a bank charge (money out) or interest (money in) draft from an unmatched line, once.
func CreateEntryFromLine(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	l, err := read(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, core.OpReconcile, core.EntityFacts(l.legalEntityID)); err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpEntryEdit, core.EntityFacts(l.legalEntityID))
	if err != nil {
		return err
	}
	if d := Lifecycle.Decide(l.State, CreateEntry); d.Effect == core.Refuse {
		return core.Refused(d, l.State)
	}
	var drafts int
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM bank_entry WHERE statement_line_id = $1 AND state <> 'cancelled'`,
			l.ID).Scan(&drafts)
	})
	if err != nil {
		return err
	}
	if drafts > 0 {
		return core.ErrState.New("an entry was made from this line already")
	}
	kind, amount := "interest", l.Amount
	if strings.HasPrefix(amount, "-") {
		kind, amount = "charge", strings.TrimPrefix(amount, "-")
	}
	text := "对账单行"
	if l.Text != nil && strings.TrimSpace(*l.Text) != "" {
		text = *l.Text
	}
	ch, err := entries.Check(m, ctx, entries.EntryIn{LegalEntityID: l.legalEntityID, Kind: kind, BankAccountID: l.BankAccountID,
		Amount: amount, EntryDate: l.BookedOn, Description: text})
	if err != nil {
		return err
	}
	id, err := entries.Create(m, ctx, req, actor, ch, &l.ID)
	if err != nil {
		return err
	}
	var e entries.Entry
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		e, err = entries.Get(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, e)
}

// Reconciliation is an account's lines over a date range: their total, matched and unmatched, and this side's
// documents no line matched.
func Reconciliation(m *core.Base, c *httpx.Context) error {
	from, to := c.Query("from"), c.Query("to")
	if !core.DateForm.MatchString(from) || !core.DateForm.MatchString(to) {
		return core.Invalid("from", "from and to are YYYY-MM-DD")
	}
	ctx, _, a, err := account(m, c, c.Param("id"), core.OpStatementRead)
	if err != nil {
		return err
	}
	var total, matched, unmatched string
	var docs []matching.Doc
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount), 0)::text,
			COALESCE(sum(amount) FILTER (WHERE state = 'matched'), 0)::text,
			COALESCE(sum(amount) FILTER (WHERE state <> 'matched'), 0)::text
			FROM statement_line WHERE bank_account_id = $1 AND booked_on BETWEEN $2::date AND $3::date`, a.ID, from, to).
			Scan(&total, &matched, &unmatched); err != nil {
			return err
		}
		var err error
		docs, err = matching.Unmatched(ctx, tx, a.ID, from, to)
		return err
	})
	if err != nil {
		return err
	}
	list := []map[string]any{}
	for _, d := range docs {
		list = append(list, map[string]any{"document_type": d.Type, "document_id": d.ID, "number": d.Number,
			"amount": reduce(d.Amount), "date": d.Date})
	}
	return c.JSON(200, map[string]any{"bank_account_id": a.ID, "from": from, "to": to, "statement_total": reduce(total),
		"matched_total": reduce(matched), "unmatched_total": reduce(unmatched), "unmatched_documents": list})
}

// reduce writes a decimal without trailing zeros.
func reduce(v string) string {
	d, err := money.Parse(strings.TrimPrefix(v, "-"))
	if err != nil {
		return v
	}
	d.Reduce(d)
	if strings.HasPrefix(v, "-") && d.Sign() != 0 {
		return "-" + d.Text('f')
	}
	return d.Text('f')
}
