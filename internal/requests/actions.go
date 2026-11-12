package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	core "github.com/brickKit/erp-payment/v3/internal/core"
	facts "github.com/brickKit/erp-payment/v3/internal/facts"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"
	matching "github.com/brickKit/erp-payment/v3/internal/matching"
	policies "github.com/brickKit/erp-payment/v3/internal/policies"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/ids"
	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
)

type paidLine struct {
	LineID string `json:"line_id"`
	Paid   string `json:"paid"`
}

type actionIn struct {
	Version       *int64     `json:"version"`
	Reason        *string    `json:"reason"`
	PaidAmount    *string    `json:"paid_amount"`
	PaidOn        *string    `json:"paid_on"`
	BankReference *string    `json:"bank_reference"`
	Lines         []paidLine `json:"lines"`
}

// ops are the operations the commands are granted under.
var ops = map[string]string{Submit: core.OpRequestEdit, Cancel: core.OpRequestEdit, Approve: core.OpRequestApprove,
	Reject: core.OpRequestApprove, Release: core.OpRequestExecute, Execute: core.OpRequestExecute, Return: core.OpRequestExecute}

// Action is one of the commands on a request, as the table decides it in the request's state: the calls to
// erp/finance (claim, confirm, release) before the transaction, the change in it.
func Action(m *core.Base, command string) httpx.Handler {
	return func(c *httpx.Context) error {
		var in actionIn
		if err := core.BindOptional(c, &in); err != nil {
			return err
		}
		if (command == Reject || command == Return) && (in.Reason == nil || strings.TrimSpace(*in.Reason) == "") {
			return core.Invalid("reason", "it says why")
		}
		id := c.Param("id")
		if !core.UUIDForm.MatchString(id) {
			return core.NotFound("payment request")
		}
		cur, err := Read(m, c.Ctx(), id)
		if err != nil {
			return err
		}
		ctx, actor, err := core.Check(c, ops[command], cur.facts())
		if err != nil {
			return err
		}
		d := Lifecycle.Decide(cur.State, command)
		switch d.Effect {
		case core.Ignore:
			return c.JSON(200, cur)
		case core.Refuse:
			return core.Refused(d, cur.State)
		}
		if in.Version != nil && *in.Version != cur.Version {
			return core.ErrVersion.New(fmt.Sprintf("the request is at version %d", cur.Version))
		}
		if d.Guard == GuardFlow { // a decision on the approval task (PR03)
			return decide(m, c, ctx, cur, command, in.Reason)
		}
		var out Request
		switch command {
		case Submit:
			out, err = submit(m, ctx, cur, actor)
		case Reject, Cancel:
			out, err = giveBack(m, ctx, cur, actor, command, in.Reason)
		case Release:
			out, err = release(m, ctx, cur, actor)
		case Execute:
			out, err = execute(m, ctx, cur, actor, in)
		case Return:
			out, err = returned(m, ctx, cur, actor, *in.Reason)
		}
		if err != nil {
			return err
		}
		return c.JSON(200, out)
	}
}

// moved runs the transaction of a command: the request locked, the table asked again (a concurrent change wins: the
// request as it is now is the answer), then write.
func moved(m *core.Base, ctx context.Context, id, command, actor string, write func(tx db.Tx, r Request) error) (Request, bool, error) {
	var out Request
	moved := false
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		r, err := Get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		out = r
		d := Lifecycle.Decide(r.State, command)
		switch d.Effect {
		case core.Ignore:
			return nil
		case core.Refuse:
			return core.Refused(d, r.State)
		}
		if err := write(tx, r); err != nil {
			return err
		}
		moved = true
		op := d.Op
		out, err = changed(m, ctx, tx, actor, op, id)
		return err
	})
	return out, moved, err
}

// payable checks every line's open item against erp/finance's due items of the payee in the request's kind:
// the payee's, in the request's currency (PR02; finance's claim does not know the payee).
func payable(m *core.Base, ctx context.Context, r Request) error {
	want := map[string]bool{}
	for _, l := range r.Lines {
		want[*l.OpenItemID] = true
	}
	kind := "payable"
	if r.Kind == "refund" {
		kind = "credit_balance"
	}
	cursor := ""
	for len(want) > 0 {
		res, err := masterdata.DueItems(m, ctx, &financev3.DueItemsRequest{LegalEntityId: r.LegalEntityID, Kind: kind,
			PartnerId: r.PayeeID, DueOnOrBefore: "9999-12-31", Cursor: cursor, Limit: 200})
		if err != nil {
			return err
		}
		for _, o := range res.GetItems() {
			if want[o.GetId()] && o.GetCurrency() == r.Currency && o.GetKind() == kind {
				delete(want, o.GetId())
			}
		}
		if cursor = res.GetNextCursor(); cursor == "" {
			break
		}
	}
	for item := range want {
		return core.ErrOpenItem.New("open item " + item + " is not the payee's " + kind + " in " + r.Currency +
			" with an available balance")
	}
	return nil
}

// releaseAll gives the claims back (idempotent at erp/finance).
func releaseAll(m *core.Base, ctx context.Context, claims []string) error {
	for _, c := range claims {
		if err := masterdata.Release(m, ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// submit is PR02: the payee's items checked, every line claimed (all or none), then submitted.
func submit(m *core.Base, ctx context.Context, cur Request, actor string) (Request, error) {
	approver, err := policies.ApproverOf(m, ctx, cur.LegalEntityID) // before anything is claimed
	if err != nil {
		return cur, err
	}
	withFinance := masterdata.Deployed(m, core.Finance)
	claimed := map[string][2]string{} // line -> claim, its end
	if withFinance {
		if err := payable(m, ctx, cur); err != nil {
			return cur, err
		}
		var made []string
		for _, l := range cur.Lines {
			claim := ids.New().String()
			end, err := masterdata.Claim(m, ctx, claim, *l.OpenItemID, l.Amount)
			if err != nil {
				if rerr := releaseAll(m, ctx, made); rerr != nil {
					m.RT.Logger.Warn("claims not released after a refused submission", "request", cur.ID, "error", rerr)
				}
				return cur, err
			}
			made = append(made, claim)
			claimed[l.ID] = [2]string{claim, end}
		}
	}
	out, moved, err := moved(m, ctx, cur.ID, Submit, actor, func(tx db.Tx, r Request) error {
		for line, c := range claimed {
			if _, err := tx.Exec(ctx, `UPDATE payment_request_line SET claim_id = $2, claim_expires_at = $3::timestamptz
				WHERE id = $1`, line, c[0], c[1]); err != nil {
				return err
			}
			if err := scheduleRenewal(m, ctx, tx, r.ID, c[0], c[1]); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_request SET state = $2, applicant_id = $3, reason = NULL,
			version = version + 1, updated_at = now() WHERE id = $1`, r.ID, Submitted, actor); err != nil {
			return err
		}
		return startApproval(m, ctx, tx, r, approver, actor)
	})
	if err == nil && !moved && len(claimed) > 0 { // submitted meanwhile by another call: these claims are not the request's
		var made []string
		for _, c := range claimed {
			made = append(made, c[0])
		}
		err = releaseAll(m, ctx, made)
	}
	return out, err
}

// giveBack is the rejection of an approved request back to draft with its reason (PR03) or a cancellation (PR07):
// the claims released first, the approval withdrawn or superseded.
func giveBack(m *core.Base, ctx context.Context, cur Request, actor, command string, reason *string) (Request, error) {
	if err := releaseAll(m, ctx, cur.claims()); err != nil {
		return cur, err
	}
	to := Draft
	if command == Cancel {
		to = Cancelled
	}
	out, _, err := moved(m, ctx, cur.ID, command, actor, func(tx db.Tx, r Request) error {
		if err := endApproval(m, ctx, tx, r); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_request_line SET claim_id = NULL, claim_expires_at = NULL
			WHERE request_id = $1`, r.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE payment_request SET state = $2, reason = COALESCE($3, reason), version = version + 1,
			updated_at = now() WHERE id = $1`, r.ID, to, reason)
		return err
	})
	return out, err
}

// release is PR04: not by the applicant's person, the approval covering the request; every claim confirmed at
// erp/finance before the money goes out.
func release(m *core.Base, ctx context.Context, cur Request, actor string) (Request, error) {
	if err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		if err := notApplicant(m, ctx, tx, cur, actor); err != nil {
			return err
		}
		return released(m, ctx, tx, cur)
	}); err != nil {
		return cur, err
	}
	if masterdata.Deployed(m, core.Finance) {
		for _, c := range cur.claims() {
			if err := masterdata.Confirm(m, ctx, c); err != nil {
				if core.ClaimRefused(err) { // the request cannot go out: its claims released, back to draft (PR04)
					if lerr := lapse(m, ctx, cur); lerr != nil {
						return cur, lerr
					}
					return cur, core.ErrClaimLost.New("claim " + c + " is no longer held: " + err.Error())
				}
				return cur, err
			}
		}
	}
	out, _, err := moved(m, ctx, cur.ID, Release, actor, func(tx db.Tx, r Request) error {
		if err := released(m, ctx, tx, r); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_request_line SET claim_expires_at = NULL WHERE request_id = $1`, r.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE payment_request SET state = $2, releaser_id = $3, version = version + 1, updated_at = now()
			WHERE id = $1`, r.ID, Ready, actor)
		return err
	})
	return out, err
}

// split is each line's share of the paid amount: as given, or in the lines' order.
func split(cur Request, in actionIn, paid string) (map[string]string, error) {
	out := map[string]string{}
	if len(in.Lines) > 0 {
		sum := "0"
		for i, l := range in.Lines {
			var line *Line
			for j := range cur.Lines {
				if cur.Lines[j].ID == l.LineID {
					line = &cur.Lines[j]
				}
			}
			p, err := core.Decimal(fmt.Sprintf("lines[%d].paid", i), l.Paid, 6, false)
			if err != nil || line == nil || core.Cmp(p, line.Amount) > 0 {
				return nil, core.Invalid(fmt.Sprintf("lines[%d]", i), "a line of the request, paid no more than its amount")
			}
			out[l.LineID] = p
			sum = core.Sum(sum, p)
		}
		if core.Cmp(sum, paid) != 0 {
			return nil, core.Invalid("lines", "the lines' paid amounts sum to paid_amount")
		}
		return out, nil
	}
	left := paid
	for _, l := range cur.Lines {
		p := l.Amount
		if core.Cmp(left, p) < 0 {
			p = left
		}
		out[l.ID] = p
		left = core.Minus(left, p)
	}
	return out, nil
}

// execute is PR05: the payment made, registered with its fund fact (each claim with its line's paid amount).
func execute(m *core.Base, ctx context.Context, cur Request, actor string, in actionIn) (Request, error) {
	switch {
	case in.PaidAmount == nil:
		return cur, core.Invalid("paid_amount", "paid_amount is required")
	case in.PaidOn == nil || !core.DateForm.MatchString(*in.PaidOn):
		return cur, core.Invalid("paid_on", "paid_on is YYYY-MM-DD")
	case in.BankReference == nil || strings.TrimSpace(*in.BankReference) == "":
		return cur, core.Invalid("bank_reference", "bank_reference is required")
	}
	scale, err := masterdata.CurrencyScale(m, ctx, cur.Currency)
	if err != nil {
		return cur, err
	}
	paid, err := core.Decimal("paid_amount", *in.PaidAmount, scale, true)
	if err != nil {
		return cur, err
	}
	if core.Cmp(paid, cur.Amount) > 0 {
		return cur, core.Invalid("paid_amount", "no more than the request's amount")
	}
	shares, err := split(cur, in, paid)
	if err != nil {
		return cur, err
	}
	le, err := masterdata.LegalEntityOf(m, ctx, cur.LegalEntityID)
	if err != nil {
		return cur, err
	}
	rate, err := masterdata.RateOf(m, ctx, cur.Currency, le.BaseCurrency, *in.PaidOn)
	if err != nil {
		return cur, err
	}
	out, _, err := moved(m, ctx, cur.ID, Execute, actor, func(tx db.Tx, r Request) error {
		if err := released(m, ctx, tx, r); err != nil { // the approval still covers what is paid (P17.7)
			return err
		}
		factID := ids.New().String()
		f := facts.Fund{FactID: factID, Source: facts.Source{Component: core.ID, DocumentType: "payment_request",
			DocumentID: r.ID, DocumentNumber: &r.Number}, LegalEntityID: r.LegalEntityID, BusinessDate: *in.PaidOn,
			PostingDate: *in.PaidOn, RuleKey: r.Kind, Currency: r.Currency, ExchangeRate: rate,
			Dimensions: facts.Dimensions{DepartmentID: r.DepartmentID}, RecordedAt: facts.Now(), Kind: r.Kind,
			PartnerID: &r.PayeeID, CompanyAccountID: r.BankAccountID, Amount: paid, Claims: []facts.ClaimAmount{}}
		for _, l := range r.Lines {
			share := shares[l.ID]
			if share == "" {
				share = "0"
			}
			if _, err := tx.Exec(ctx, `UPDATE payment_request_line SET paid = $2 WHERE id = $1`, l.ID, share); err != nil {
				return err
			}
			if l.ClaimID != nil && core.Cmp(share, "0") > 0 {
				f.Claims = append(f.Claims, facts.ClaimAmount{ClaimID: *l.ClaimID, Amount: share})
			}
		}
		if err := f.Publish(m, ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_request SET state = $2, paid_amount = $3, paid_on = $4, bank_reference = $5,
			fact_id = $6, version = version + 1, updated_at = now() WHERE id = $1`, r.ID, Paid, paid, *in.PaidOn,
			*in.BankReference, factID); err != nil {
			return err
		}
		return matching.Document(ctx, tx, r.BankAccountID, in.BankReference) // a line waiting for it (BR01)
	})
	return out, err
}

// returned is PR06: the money did not go out or came back: the reversing fund fact refers to the payment's.
func returned(m *core.Base, ctx context.Context, cur Request, actor, reason string) (Request, error) {
	le, err := masterdata.LegalEntityOf(m, ctx, cur.LegalEntityID)
	if err != nil {
		return cur, err
	}
	loc, err := time.LoadLocation(le.Timezone)
	if err != nil {
		loc = time.UTC
	}
	today := time.Now().In(loc).Format("2006-01-02")
	out, _, err := moved(m, ctx, cur.ID, Return, actor, func(tx db.Tx, r Request) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT data::text FROM payment_fact WHERE fact_id = $1`, *r.FactID).Scan(&raw); err != nil {
			return err
		}
		var f facts.Fund
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		original, reversal := f.FactID, ids.New().String()
		f.FactID, f.Kind, f.ReversesFactID, f.RecordedAt = reversal, "reversal", &original, facts.Now()
		f.BusinessDate, f.PostingDate, f.Claims = today, today, []facts.ClaimAmount{}
		if err := f.Publish(m, ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE payment_request SET state = $2, reason = $3, reversal_fact_id = $4, version = version + 1,
			updated_at = now() WHERE id = $1`, r.ID, Returned, reason, reversal)
		return err
	})
	return out, err
}
