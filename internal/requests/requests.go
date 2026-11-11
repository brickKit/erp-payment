package requests

import (
	"context"
	"fmt"
	"strings"
	"time"

	bankaccounts "github.com/brickKit/erp-payment/v3/internal/bankaccounts"
	core "github.com/brickKit/erp-payment/v3/internal/core"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"
	orgunits "github.com/brickKit/erp-payment/v3/internal/orgunits"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/list"
	"github.com/brickKit/be-sdk-go/v3/search"
	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
)

type lineIn struct {
	OpenItemID *string `json:"open_item_id"`
	Amount     string  `json:"amount"`
}

type requestIn struct {
	LegalEntityID string   `json:"legal_entity_id"`
	Kind          string   `json:"kind"`
	PayeeID       string   `json:"payee_id"`
	BankAccountID string   `json:"bank_account_id"`
	Purpose       string   `json:"purpose"`
	Reference     *string  `json:"reference"`
	DepartmentID  *string  `json:"department_id"`
	Version       *int64   `json:"version"`
	Lines         []lineIn `json:"lines"`
}

// prepared is a request's input with its evidence, taken before the transaction (PR01).
type prepared struct {
	in       requestIn
	le       masterdata.LegalEntity
	currency string
	amounts  []string
	total    string
	deptID   *string
	deptPath *string
}

func prepare(m *core.Base, ctx context.Context, in requestIn) (prepared, error) {
	p := prepared{in: in}
	switch {
	case !core.UUIDForm.MatchString(in.LegalEntityID):
		return p, core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	case in.Kind != "payment" && in.Kind != "refund":
		return p, core.Invalid("kind", "kind is payment or refund")
	case !core.UUIDForm.MatchString(in.PayeeID):
		return p, core.Invalid("payee_id", "payee_id is a partner's ID")
	case !core.UUIDForm.MatchString(in.BankAccountID):
		return p, core.Invalid("bank_account_id", "bank_account_id is a bank account's ID")
	case strings.TrimSpace(in.Purpose) == "":
		return p, core.Invalid("purpose", "purpose is required")
	case len(in.Lines) == 0 || len(in.Lines) > core.MaxLines:
		return p, core.Invalid("lines", fmt.Sprintf("1 to %d lines", core.MaxLines))
	}
	withFinance := masterdata.Deployed(m, core.Finance)
	seen := map[string]bool{}
	for i, l := range in.Lines {
		field := fmt.Sprintf("lines[%d].open_item_id", i)
		switch {
		case withFinance && l.OpenItemID == nil:
			return p, core.Invalid(field, "each line pays one of erp/finance's open items")
		case !withFinance && l.OpenItemID != nil:
			return p, core.Invalid(field, "without erp/finance a line names no open item")
		case l.OpenItemID != nil && !core.UUIDForm.MatchString(*l.OpenItemID):
			return p, core.Invalid(field, "an open item's ID")
		case l.OpenItemID != nil && seen[*l.OpenItemID]:
			return p, core.Invalid(field, "an open item once in a request")
		}
		if l.OpenItemID != nil {
			seen[*l.OpenItemID] = true
		}
	}
	var err error
	if p.le, err = masterdata.LegalEntityOf(m, ctx, in.LegalEntityID); err != nil {
		return p, err
	}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		a, err := bankaccounts.Usable(ctx, tx, in.BankAccountID, in.LegalEntityID)
		p.currency = a.Currency
		return err
	})
	if err != nil {
		return p, err
	}
	if _, err := masterdata.PartnerOf(m, ctx, in.PayeeID); err != nil {
		return p, err
	}
	if p.deptID, p.deptPath, err = orgunits.Usable(m, ctx, in.DepartmentID, in.LegalEntityID); err != nil {
		return p, err
	}
	scale, err := masterdata.CurrencyScale(m, ctx, p.currency)
	if err != nil {
		return p, err
	}
	p.total = "0"
	for i, l := range in.Lines {
		a, err := core.Decimal(fmt.Sprintf("lines[%d].amount", i), l.Amount, scale, true)
		if err != nil {
			return p, err
		}
		p.amounts = append(p.amounts, a)
		p.total = core.Sum(p.total, a)
	}
	return p, nil
}

// writeLines replaces a draft's lines.
func writeLines(ctx context.Context, tx db.Tx, id string, p prepared) error {
	if _, err := tx.Exec(ctx, `DELETE FROM payment_request_line WHERE request_id = $1`, id); err != nil {
		return err
	}
	for i, l := range p.in.Lines {
		if _, err := tx.Exec(ctx, `INSERT INTO payment_request_line (id, request_id, position, open_item_id, amount)
			VALUES ($1, $2, $3, $4, $5)`, ids.New().String(), id, i+1, l.OpenItemID, p.amounts[i]); err != nil {
			return err
		}
	}
	return nil
}

// CreateRequest is a draft (PR01).
func CreateRequest(m *core.Base, c *httpx.Context) error {
	req, err := idem.FromHTTP(c)
	if err != nil {
		return err
	}
	var in requestIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	p, err := prepare(m, c.Ctx(), in)
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpRequestEdit, core.DocFacts(in.LegalEntityID, p.deptPath))
	if err != nil {
		return err
	}
	number, err := core.Number(m, ctx, "PR", in.LegalEntityID, p.le.Code, time.Now().Format("2006-01-02"))
	if err != nil {
		return err
	}
	out, _, err := idem.Do(ctx, m.RT.DB, req, func(ctx context.Context, tx db.Tx) (idem.Outcome, error) {
		id := ids.New().String()
		if _, err := tx.Exec(ctx, `INSERT INTO payment_request (id, number, legal_entity_id, kind, payee_id, bank_account_id,
			currency, amount, purpose, reference, department_id, department_path, maker_id, search, search_version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`, id, number, in.LegalEntityID, in.Kind,
			in.PayeeID, in.BankAccountID, p.currency, p.total, in.Purpose, in.Reference, p.deptID, p.deptPath, actor,
			search.Text(number, deref(in.Reference)), search.Version); err != nil {
			return idem.Outcome{}, err
		}
		if err := writeLines(ctx, tx, id, p); err != nil {
			return idem.Outcome{}, err
		}
		_, err := changed(m, ctx, tx, actor, core.OpRequestEdit, id)
		return core.IDResult(id), err
	})
	if err != nil {
		return err
	}
	r, err := Read(m, ctx, core.ResultID(out))
	if err != nil {
		return err
	}
	return c.JSON(200, r)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// UpdateRequest replaces a draft's content and lines, by version (PR01).
func UpdateRequest(m *core.Base, c *httpx.Context) error {
	var in requestIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	cur, err := Read(m, c.Ctx(), c.Param("id"))
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, core.OpRequestEdit, cur.facts()); err != nil {
		return err
	}
	if in.LegalEntityID != cur.LegalEntityID {
		return core.Invalid("legal_entity_id", "a request stays in its legal entity")
	}
	p, err := prepare(m, c.Ctx(), in)
	if err != nil {
		return err
	}
	ctx, actor, err := core.Check(c, core.OpRequestEdit, core.DocFacts(in.LegalEntityID, p.deptPath))
	if err != nil {
		return err
	}
	var out Request
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		r, err := Get(ctx, tx, cur.ID, true)
		if err != nil {
			return err
		}
		if d := Lifecycle.Decide(r.State, Edit); d.Effect == core.Refuse {
			return core.Refused(d, r.State)
		}
		if in.Version != nil && *in.Version != r.Version {
			return core.ErrVersion.New(fmt.Sprintf("the request is at version %d", r.Version))
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_request SET kind = $2, payee_id = $3, bank_account_id = $4, currency = $5,
			amount = $6, purpose = $7, reference = $8, department_id = $9, department_path = $10, search = $11,
			search_version = $12, version = version + 1, updated_at = now() WHERE id = $1`, r.ID, in.Kind, in.PayeeID,
			in.BankAccountID, p.currency, p.total, in.Purpose, in.Reference, p.deptID, p.deptPath,
			search.Text(r.Number, deref(in.Reference)), search.Version); err != nil {
			return err
		}
		if err := writeLines(ctx, tx, r.ID, p); err != nil {
			return err
		}
		out, err = changed(m, ctx, tx, actor, core.OpRequestEdit, r.ID)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}

func GetRequest(m *core.Base, c *httpx.Context) error {
	id := c.Param("id")
	if !core.UUIDForm.MatchString(id) {
		return core.NotFound("payment request")
	}
	r, err := Read(m, c.Ctx(), id)
	if err != nil {
		return err
	}
	if _, _, err := core.Check(c, core.OpRequestRead, r.facts()); err != nil {
		return err
	}
	return c.JSON(200, r)
}

var requestList = list.Spec{Operation: core.OpRequestRead, Alias: "r", IDColumn: "id", IDKind: list.UUID,
	Columns: []list.Column{
		{Field: "created_at", Column: "created_at", Kind: list.Time, Sortable: true},
		{Field: "legal_entity_id", Column: "legal_entity_id", Kind: list.UUID, Filterable: true},
		{Field: "payee_id", Column: "payee_id", Kind: list.UUID, Filterable: true},
		{Field: "department_id", Column: "department_id", Kind: list.UUID, Filterable: true},
		{Field: "state", Column: "state", Kind: list.Text, Filterable: true}},
	Search: []list.SearchColumn{{Name: "number_reference", Column: "search", Fields: []string{"number", "reference"}}}}

// ListRequests is one page, newest first; each with its lines.
func ListRequests(m *core.Base, c *httpx.Context) error {
	var f []list.Filter
	for _, k := range []string{"legal_entity_id", "payee_id", "department_id", "state"} {
		if v := c.Query(k); v != "" {
			f = append(f, list.Filter{Field: k, Op: "eq", Values: []string{v}})
		}
	}
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	s, _ := sdkauthz.From(c.Ctx())
	ctx, q, err := list.Build(c.Ctx(), s, requestList, list.Request{Filters: f, Q: c.Query("q"), Limit: limit,
		Cursor: c.Query("cursor"), Sort: []list.Order{{Field: "created_at", Desc: true}}})
	if err != nil {
		return err
	}
	out, next, err := list.Page(ctx, m.RT.DB, q, "payment_request", cols, scan,
		func(r Request) ([]string, string) { return []string{r.createdAt}, r.ID })
	if err != nil {
		return err
	}
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		for i := range out {
			if out[i].Lines, err = lines(ctx, tx, out[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.JSON(200, map[string]any{"requests": out, "next_cursor": next})
}

// ListProposals is erp/finance's due open items for payment requests (its DueItems): the payables and credit
// balances of the legal entity with an available balance, earliest due first.
func ListProposals(m *core.Base, c *httpx.Context) error {
	le := c.Query("legal_entity_id")
	if !core.UUIDForm.MatchString(le) {
		return core.Invalid("legal_entity_id", "legal_entity_id is a legal entity's ID")
	}
	ctx, _, err := core.Check(c, core.OpRequestRead, core.EntityFacts(le))
	if err != nil {
		return err
	}
	before := c.Query("due_on_or_before")
	if before == "" {
		before = time.Now().Format("2006-01-02")
	}
	limit, err := list.ParseLimit(c.Query("limit"))
	if err != nil {
		return err
	}
	res, err := masterdata.DueItems(m, ctx, &financev3.DueItemsRequest{LegalEntityId: le, Kind: c.Query("kind"),
		PartnerId: c.Query("partner_id"), DueOnOrBefore: before, Cursor: c.Query("cursor"), Limit: int32(limit)})
	if err != nil {
		return err
	}
	items := []map[string]any{}
	for _, o := range res.GetItems() {
		var number *string
		if o.GetSourceNumber() != "" {
			n := o.GetSourceNumber()
			number = &n
		}
		items = append(items, map[string]any{"open_item_id": o.GetId(), "kind": o.GetKind(), "partner_id": o.GetPartnerId(),
			"currency": o.GetCurrency(), "available": o.GetAvailable(), "due_date": o.GetDueDate(), "source_number": number})
	}
	return c.JSON(200, map[string]any{"items": items, "next_cursor": res.GetNextCursor()})
}
