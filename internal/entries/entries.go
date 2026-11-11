package entries

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	bankaccounts "github.com/brickKit/erp-payment/v3/internal/bankaccounts"
	core "github.com/brickKit/erp-payment/v3/internal/core"
	facts "github.com/brickKit/erp-payment/v3/internal/facts"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/list"
	"github.com/jackc/pgx/v5"
)

// Entry is a transfer, a bank charge or interest as the user surface reads it.
type Entry struct {
	ID              string  `json:"id"`
	Number          string  `json:"number"`
	LegalEntityID   string  `json:"legal_entity_id"`
	Kind            string  `json:"kind"`
	BankAccountID   string  `json:"bank_account_id"`
	ToBankAccountID *string `json:"to_bank_account_id"`
	Currency        string  `json:"currency"`
	Amount          string  `json:"amount"`
	EntryDate       string  `json:"entry_date"`
	ArrivedOn       *string `json:"arrived_on"`
	Description     string  `json:"description"`
	State           string  `json:"state"`
	MakerID         string  `json:"maker_id"`
	ApproverID      *string `json:"approver_id"`
	StatementLineID *string `json:"statement_line_id"`
	Reason          *string `json:"reason"`
	Version         int64   `json:"version"`
	UpdatedAt       string  `json:"updated_at"`
	createdAt       string
}

const cols = `e.id::text, e.number, e.legal_entity_id::text, e.kind, e.bank_account_id::text, e.to_bank_account_id::text,
	e.currency, e.amount::text, e.entry_date::text, e.arrived_on::text, e.description, e.state, e.maker_id, e.approver_id,
	e.statement_line_id::text, e.reason, e.version, e.updated_at, e.created_at`

func scan(row pgx.Row) (Entry, error) {
	var e Entry
	var at, created time.Time
	err := row.Scan(&e.ID, &e.Number, &e.LegalEntityID, &e.Kind, &e.BankAccountID, &e.ToBankAccountID, &e.Currency, &e.Amount,
		&e.EntryDate, &e.ArrivedOn, &e.Description, &e.State, &e.MakerID, &e.ApproverID, &e.StatementLineID, &e.Reason,
		&e.Version, &at, &created)
	e.UpdatedAt, e.createdAt = canon.Time(at), canon.Time(created)
	return e, err
}

// Get reads an entry (FOR UPDATE when lock).
func Get(ctx context.Context, tx db.Tx, id string, lock bool) (Entry, error) {
	q := `SELECT ` + cols + ` FROM bank_entry e WHERE e.id = $1::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	e, err := scan(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return e, core.NotFound("bank entry")
	}
	return e, err
}

func read(m *core.Base, ctx context.Context, id string) (Entry, error) {
	var e Entry
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		e, err = Get(ctx, tx, id, false)
		return err
	})
	return e, err
}

// EntryIn is a draft's content (the user's, or one made from a statement line, BR04).
type EntryIn struct {
	LegalEntityID   string  `json:"legal_entity_id"`
	Kind            string  `json:"kind"`
	BankAccountID   string  `json:"bank_account_id"`
	ToBankAccountID *string `json:"to_bank_account_id"`
	Amount          string  `json:"amount"`
	EntryDate       string  `json:"entry_date"`
	Description     string  `json:"description"`
	Version         *int64  `json:"version"`
}

// checked is a draft's input with its evidence (BE01): the accounts usable, of one currency for a transfer.
type checked struct {
	in       EntryIn
	le       masterdata.LegalEntity
	currency string
	amount   string
}

func check(m *core.Base, ctx context.Context, in EntryIn) (checked, error) {
	ch := checked{in: in}
	switch {
	case !core.UUIDForm.MatchString(in.LegalEntityID):
		return ch, core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	case in.Kind != "transfer" && in.Kind != "charge" && in.Kind != "interest":
		return ch, core.Invalid("kind", "kind is transfer, charge or interest")
	case !core.UUIDForm.MatchString(in.BankAccountID):
		return ch, core.Invalid("bank_account_id", "bank_account_id is a bank account's ID")
	case in.Kind == "transfer" && (in.ToBankAccountID == nil || !core.UUIDForm.MatchString(*in.ToBankAccountID)):
		return ch, core.Invalid("to_bank_account_id", "a transfer names the account it goes to")
	case in.Kind != "transfer" && in.ToBankAccountID != nil:
		return ch, core.Invalid("to_bank_account_id", "only a transfer names an account it goes to")
	case in.Kind == "transfer" && *in.ToBankAccountID == in.BankAccountID:
		return ch, core.Invalid("to_bank_account_id", "a transfer goes to another account")
	case !core.DateForm.MatchString(in.EntryDate):
		return ch, core.Invalid("entry_date", "entry_date is YYYY-MM-DD")
	case strings.TrimSpace(in.Description) == "":
		return ch, core.Invalid("description", "description is required")
	}
	var err error
	if ch.le, err = masterdata.LegalEntityOf(m, ctx, in.LegalEntityID); err != nil {
		return ch, err
	}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		from, err := bankaccounts.Usable(ctx, tx, in.BankAccountID, in.LegalEntityID)
		if err != nil {
			return err
		}
		ch.currency = from.Currency
		if in.ToBankAccountID == nil {
			return nil
		}
		to, err := bankaccounts.Usable(ctx, tx, *in.ToBankAccountID, in.LegalEntityID)
		if err != nil {
			return err
		}
		if to.Currency != from.Currency {
			return core.Invalid("to_bank_account_id", "a transfer stays in one currency")
		}
		return nil
	})
	if err != nil {
		return ch, err
	}
	scale, err := masterdata.CurrencyScale(m, ctx, ch.currency)
	if err != nil {
		return ch, err
	}
	ch.amount, err = core.Decimal("amount", in.Amount, scale, true)
	return ch, err
}

// Create makes a draft as actor in its own idempotent transaction; line is the statement line it comes from (BR04).
func Create(m *core.Base, ctx context.Context, req idem.Request, actor string, ch checked, line *string) (string, error) {
	number, err := core.Number(m, ctx, "BE", ch.in.LegalEntityID, ch.le.Code, ch.in.EntryDate)
	if err != nil {
		return "", err
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		id := ids.New().String()
		if _, err := tx.Exec(ctx, `INSERT INTO bank_entry (id, number, legal_entity_id, kind, bank_account_id, to_bank_account_id,
			currency, amount, entry_date, description, maker_id, statement_line_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12)`, id, number, ch.in.LegalEntityID, ch.in.Kind, ch.in.BankAccountID, ch.in.ToBankAccountID,
			ch.currency, ch.amount, ch.in.EntryDate, ch.in.Description, actor, line); err != nil {
			return idem.Outcome{}, err
		}
		_, err := changed(m, ctx, tx, actor, core.OpEntryEdit, id)
		return core.IDResult(id), err
	})
	return core.ResultID(out), err
}

// Check is the evidence of a draft (BR04 makes one from a statement line).
func Check(m *core.Base, ctx context.Context, in EntryIn) (checked, error) { return check(m, ctx, in) }

// changed audits the entry in the transaction that changed it.
func changed(m *core.Base, ctx context.Context, tx db.Tx, actor, op, id string) (Entry, error) {
	e, err := Get(ctx, tx, id, false)
	if err != nil {
		return e, err
	}
	return e, core.Change(m, ctx, tx, actor, op, core.Entries, id, e)
}

// CreateEntry is a draft (BE01).
func CreateEntry(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	var in EntryIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpEntryEdit, core.EntityFacts(in.LegalEntityID))
	if err != nil {
		return err
	}
	ch, err := check(m, ctx, in)
	if err != nil {
		return err
	}
	id, err := Create(m, ctx, req, actor, ch, nil)
	if err != nil {
		return err
	}
	e, err := read(m, ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(200, e)
}

// UpdateEntry changes a draft, by version (BE01).
func UpdateEntry(m *core.Base, c *httpx.Context) error {
	var in EntryIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	cur, err := read(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpEntryEdit, core.EntityFacts(cur.LegalEntityID))
	if err != nil {
		return err
	}
	if in.LegalEntityID != cur.LegalEntityID {
		return core.Invalid("legal_entity_id", "an entry stays in its legal entity")
	}
	ch, err := check(m, ctx, in)
	if err != nil {
		return err
	}
	var out Entry
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		e, err := Get(ctx, tx, cur.ID, true)
		if err != nil {
			return err
		}
		if d := Lifecycle.Decide(e.State, Edit); d.Effect == core.Refuse {
			return core.Refused(d, e.State)
		}
		if in.Version != nil && *in.Version != e.Version {
			return core.ErrVersion.New(fmt.Sprintf("the entry is at version %d", e.Version))
		}
		if _, err := tx.Exec(ctx, `UPDATE bank_entry SET kind = $2, bank_account_id = $3, to_bank_account_id = $4, currency = $5,
			amount = $6, entry_date = $7, description = $8, version = version + 1, updated_at = now() WHERE id = $1`, e.ID,
			in.Kind, in.BankAccountID, in.ToBankAccountID, ch.currency, ch.amount, in.EntryDate, in.Description); err != nil {
			return err
		}
		out, err = changed(m, ctx, tx, actor, core.OpEntryEdit, e.ID)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}

func GetEntry(m *core.Base, c *httpx.Context) error {
	id := c.Param("id")
	if !core.UUIDForm.MatchString(id) {
		return core.NotFound("bank entry")
	}
	e, err := read(m, c.Ctx(), id)
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, core.OpEntryRead, core.EntityFacts(e.LegalEntityID)); err != nil {
		return err
	}
	return c.JSON(200, e)
}

var entryList = list.Spec{Operation: core.OpEntryRead, Alias: "e", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "created_at", Column: "created_at", Kind: list.Time, Sortable: true},
		{Field: "legal_entity_id", Column: "legal_entity_id", Kind: list.UUID, Filterable: true},
		{Field: "bank_account_id", Column: "bank_account_id", Kind: list.UUID, Filterable: true},
		{Field: "kind", Column: "kind", Kind: list.Text, Filterable: true},
		{Field: "state", Column: "state", Kind: list.Text, Filterable: true}}}

// ListEntries is one page, newest first.
func ListEntries(m *core.Base, c *httpx.Context) error {
	var f []list.Filter
	for _, k := range []string{"legal_entity_id", "bank_account_id", "kind", "state"} {
		if v := c.Query(k); v != "" {
			f = append(f, list.Filter{Field: k, Op: "eq", Values: []string{v}})
		}
	}
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	s, _ := sdkauthz.From(c.Ctx())
	ctx, q, err := list.Build(c.Ctx(), s, entryList, list.Request{Filters: f, Limit: limit, Cursor: c.Query("cursor"),
		Sort: []list.Order{{Field: "created_at", Desc: true}}})
	if err != nil {
		return err
	}
	out, next, err := list.Page(ctx, m.RT.DB, q, "bank_entry", cols, scan,
		func(e Entry) ([]string, string) { return []string{e.createdAt}, e.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"entries": out, "next_cursor": next})
}

// fact is an entry's accounting fact: rule with its roles' amount, on date.
func fact(e Entry, rule, date string, roles []string, rate *masterdata.Rate) facts.Accounting {
	amounts := map[string]string{}
	for _, r := range roles {
		amounts[r] = e.Amount
	}
	return facts.Accounting{FactID: ids.New().String(), Source: facts.Source{Component: core.ID, DocumentType: "bank_entry",
		DocumentID: e.ID, DocumentNumber: &e.Number}, LegalEntityID: e.LegalEntityID, BusinessDate: date, PostingDate: date,
		RuleKey: rule, Currency: e.Currency, ExchangeRate: rate, Amounts: amounts, RecordedAt: facts.Now()}
}
