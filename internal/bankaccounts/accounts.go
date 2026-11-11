package bankaccounts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	core "github.com/brickKit/erp-payment/v3/internal/core"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"

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

// Account is a company bank account as the user surface reads it; Number is whole or masked (Mask).
type Account struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legal_entity_id"`
	Name          string `json:"name"`
	Bank          string `json:"bank"`
	Number        string `json:"number"`
	Currency      string `json:"currency"`
	State         string `json:"state"`
	Version       int64  `json:"version"`
	UpdatedAt     string `json:"updated_at"`
}

var numberForm = regexp.MustCompile(`^[0-9A-Z -]{4,40}$`)

// Mask shows a number's last four digits only, to a caller whose grant does not list the restricted field.
func (a Account) Mask(readable []string) Account {
	if !slices.Contains(readable, "number") {
		a.Number = "****" + a.Number[max(0, len(a.Number)-4):]
	}
	return a
}

const cols = `a.id::text, a.legal_entity_id::text, a.name, a.bank, a.number, a.currency, a.state, a.version, a.updated_at`

func scan(row pgx.Row, more ...any) (Account, error) {
	var a Account
	var at time.Time
	err := row.Scan(append([]any{&a.ID, &a.LegalEntityID, &a.Name, &a.Bank, &a.Number, &a.Currency, &a.State, &a.Version,
		&at}, more...)...)
	a.UpdatedAt = canon.Time(at)
	return a, err
}

// Get reads an account (FOR SHARE when lock: a document registered on it holds it).
func Get(ctx context.Context, tx db.Tx, id string, lock bool) (Account, error) {
	q := `SELECT ` + cols + ` FROM bank_account a WHERE a.id = $1::uuid`
	if lock {
		q += ` FOR SHARE`
	}
	a, err := scan(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, core.NotFound("bank account")
	}
	return a, err
}

// Usable is an account a document of the legal entity is registered on: it exists, is the legal entity's, is active
// (held FOR SHARE until the transaction ends).
func Usable(ctx context.Context, tx db.Tx, id, legalEntity string) (Account, error) {
	a, err := Get(ctx, tx, id, true)
	if err != nil {
		return a, core.ErrBankAccount.New("no bank account " + id)
	}
	if a.LegalEntityID != legalEntity || a.State != "active" {
		return a, core.ErrBankAccount.New(fmt.Sprintf("bank account %s is %s, of legal entity %s", id, a.State, a.LegalEntityID))
	}
	return a, nil
}

type accountIn struct {
	LegalEntityID string `json:"legal_entity_id"`
	Name          string `json:"name"`
	Bank          string `json:"bank"`
	Number        string `json:"number"`
	Currency      string `json:"currency"`
}

// CreateBankAccount adds a legal entity's account: the legal entity and the currency are active; a number once in the
// legal entity (CODE_TAKEN).
func CreateBankAccount(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	var in accountIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	switch {
	case !core.UUIDForm.MatchString(in.LegalEntityID):
		return core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	case strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Bank) == "":
		return core.Invalid("name", "name and bank are required")
	case !numberForm.MatchString(in.Number):
		return core.Invalid("number", "number is 4-40 of 0-9, A-Z, space and '-'")
	case !core.CurrencyForm.MatchString(in.Currency):
		return core.Invalid("currency", "currency is an ISO 4217 code")
	}
	ctx, actor, err := core.Check(c, core.OpBankAccountWrite, core.EntityFacts(in.LegalEntityID))
	if err != nil {
		return err
	}
	if _, err := masterdata.LegalEntityOf(m, ctx, in.LegalEntityID); err != nil {
		return err
	}
	if _, err := masterdata.CurrencyScale(m, ctx, in.Currency); err != nil {
		return err
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		id := ids.New().String()
		tag, err := tx.Exec(ctx, `INSERT INTO bank_account (id, legal_entity_id, name, bank, number, currency, search,
			search_version) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (legal_entity_id, number) DO NOTHING`,
			id, in.LegalEntityID, in.Name, in.Bank, in.Number, in.Currency, search.Text(in.Name, in.Bank), search.Version)
		if err != nil {
			return idem.Outcome{}, err
		}
		if tag.RowsAffected() == 0 {
			return idem.Outcome{}, core.ErrCode.New("the account number is registered in this legal entity already")
		}
		a, err := Get(ctx, tx, id, false)
		if err != nil {
			return idem.Outcome{}, err
		}
		return core.IDResult(id), core.Change(m, ctx, tx, actor, core.OpBankAccountWrite, core.BankAccounts, id, a.Mask(nil)) // no whole number in the audit
	})
	if err != nil {
		return err
	}
	return read(m, c, ctx, core.ResultID(out))
}

// read answers an account as the caller may see it.
func read(m *core.Base, c *httpx.Context, ctx context.Context, id string) error {
	var a Account
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		a, err = Get(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	s, _ := sdkauthz.From(c.Ctx())
	_, d, err := s.Check(c.Ctx(), core.OpBankAccountRead, core.EntityFacts(a.LegalEntityID))
	if err != nil {
		return err
	}
	return c.JSON(200, a.Mask(d.Fields))
}

func GetBankAccount(m *core.Base, c *httpx.Context) error {
	id := c.Param("id")
	if !core.UUIDForm.MatchString(id) {
		return core.NotFound("bank account")
	}
	return read(m, c, c.Ctx(), id)
}

type accountPatch struct {
	Name    *string `json:"name"`
	State   *string `json:"state"`
	Version *int64  `json:"version"`
}

// UpdateBankAccount changes an account's name or state, by version.
func UpdateBankAccount(m *core.Base, c *httpx.Context) error {
	var in accountPatch
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return core.Invalid("name", "name is required")
	}
	if in.State != nil && *in.State != "active" && *in.State != "inactive" {
		return core.Invalid("state", "state is active or inactive")
	}
	id := c.Param("id")
	if !core.UUIDForm.MatchString(id) {
		return core.NotFound("bank account")
	}
	var a Account
	err := m.RT.DB.Tx(c.Ctx(), func(ctx context.Context, tx db.Tx) error {
		var err error
		a, err = Get(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpBankAccountWrite, core.EntityFacts(a.LegalEntityID))
	if err != nil {
		return err
	}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM bank_account WHERE id = $1 FOR UPDATE`, id); err != nil {
			return err
		}
		cur, err := Get(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if in.Version != nil && *in.Version != cur.Version {
			return core.ErrVersion.New(fmt.Sprintf("the bank account is at version %d", cur.Version))
		}
		name, state := cur.Name, cur.State
		if in.Name != nil {
			name = *in.Name
		}
		if in.State != nil {
			state = *in.State
		}
		if _, err := tx.Exec(ctx, `UPDATE bank_account SET name = $2, state = $3, search = $4, search_version = $5,
			version = version + 1, updated_at = now() WHERE id = $1`, id, name, state, search.Text(name, cur.Bank),
			search.Version); err != nil {
			return err
		}
		if a, err = Get(ctx, tx, id, false); err != nil {
			return err
		}
		return core.Change(m, ctx, tx, actor, core.OpBankAccountWrite, core.BankAccounts, id, a.Mask(nil)) // no whole number in the audit
	})
	if err != nil {
		return err
	}
	return read(m, c, ctx, id)
}

var accountList = list.Spec{Operation: core.OpBankAccountRead, Alias: "a", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "name", Column: "name", Kind: list.Text, Sortable: true},
		{Field: "legal_entity_id", Column: "legal_entity_id", Kind: list.UUID, Filterable: true},
		{Field: "currency", Column: "currency", Kind: list.Text, Filterable: true},
		{Field: "state", Column: "state", Kind: list.Text, Filterable: true}},
	Search: []list.SearchColumn{{Name: "name_bank", Column: "search", Fields: []string{"name", "bank"}}}}

// ListBankAccounts is one page by name; each row's number whole or masked by what the caller reads on that row.
func ListBankAccounts(m *core.Base, c *httpx.Context) error {
	var f []list.Filter
	for _, k := range []string{"legal_entity_id", "currency", "state"} {
		if v := c.Query(k); v != "" {
			f = append(f, list.Filter{Field: k, Op: "eq", Values: []string{v}})
		}
	}
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	s, _ := sdkauthz.From(c.Ctx())
	ctx, q, err := list.Build(c.Ctx(), s, accountList, list.Request{Filters: f, Q: c.Query("q"), Limit: limit,
		Cursor: c.Query("cursor"), Sort: []list.Order{{Field: "name"}}})
	if err != nil {
		return err
	}
	selected := cols
	if len(q.Hits) > 0 {
		selected += ", " + strings.Join(q.Hits, ", ")
	}
	out, next, err := list.Page(ctx, m.RT.DB, q, "bank_account", selected, func(row pgx.Row) (Account, error) {
		hits := make([]bool, len(q.Hits))
		ptrs := make([]any, len(hits))
		for i := range hits {
			ptrs[i] = &hits[i]
		}
		a, err := scan(row, ptrs...)
		return a.Mask(q.Fields(hits)), err
	}, func(a Account) ([]string, string) { return []string{a.Name}, a.ID })
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"bank_accounts": out, "next_cursor": next})
}
