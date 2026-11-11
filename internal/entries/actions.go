package entries

import (
	"context"
	"fmt"
	"strings"

	core "github.com/brickKit/erp-payment/v3/internal/core"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"
	matching "github.com/brickKit/erp-payment/v3/internal/matching"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/httpx"
)

type actionIn struct {
	Version   *int64  `json:"version"`
	Reason    *string `json:"reason"`
	ArrivedOn *string `json:"arrived_on"`
}

var ops = map[string]string{Submit: core.OpEntryEdit, Cancel: core.OpEntryEdit, Arrive: core.OpEntryEdit,
	Approve: core.OpEntryApprove, Reject: core.OpEntryApprove}

// Action is one of submit, approve, reject, arrive and cancel, as the table decides it in the entry's state.
func Action(m *core.Base, command string) httpx.Handler {
	return func(c *httpx.Context) error {
		var in actionIn
		if err := core.BindOptional(c, &in); err != nil {
			return err
		}
		if command == Reject && (in.Reason == nil || strings.TrimSpace(*in.Reason) == "") {
			return core.Invalid("reason", "a rejection says why")
		}
		cur, err := read(m, c.Ctx(), c.Param("id"))
		if err != nil {
			return err
		}
		ctx, actor, err := core.Check(c, ops[command], core.EntityFacts(cur.LegalEntityID))
		if err != nil {
			return err
		}
		if command == Arrive && cur.State == InTransit &&
			(in.ArrivedOn == nil || !core.DateForm.MatchString(*in.ArrivedOn) || *in.ArrivedOn < cur.EntryDate) {
			return core.Invalid("arrived_on", "an arrival date (YYYY-MM-DD) not before the transfer's")
		}
		var rate *masterdata.Rate
		if command == Approve || command == Arrive {
			le, err := masterdata.LegalEntityOf(m, ctx, cur.LegalEntityID)
			if err != nil {
				return err
			}
			date := cur.EntryDate
			if command == Arrive && in.ArrivedOn != nil {
				date = *in.ArrivedOn
			}
			if rate, err = masterdata.RateOf(m, ctx, cur.Currency, le.BaseCurrency, date); err != nil {
				return err
			}
		}
		var out Entry
		err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
			e, err := Get(ctx, tx, cur.ID, true)
			if err != nil {
				return err
			}
			out = e
			d := Lifecycle.Decide(e.State, command)
			switch d.Effect {
			case core.Ignore:
				return nil
			case core.Refuse:
				return core.Refused(d, e.State)
			}
			if in.Version != nil && *in.Version != e.Version {
				return core.ErrVersion.New(fmt.Sprintf("the entry is at version %d", e.Version))
			}
			if err := apply(m, ctx, tx, e, command, actor, in, rate); err != nil {
				return err
			}
			out, err = changed(m, ctx, tx, actor, d.Op, e.ID)
			return err
		})
		if err != nil {
			return err
		}
		return c.JSON(200, out)
	}
}

// apply writes one command's change, with the accounting fact of an approval or an arrival (BE02, BE03).
func apply(m *core.Base, ctx context.Context, tx db.Tx, e Entry, command, actor string, in actionIn, rate *masterdata.Rate) error {
	set := func(sql string, args ...any) error {
		_, err := tx.Exec(ctx, `UPDATE bank_entry SET `+sql+`, version = version + 1, updated_at = now() WHERE id = $1`,
			append([]any{e.ID}, args...)...)
		return err
	}
	switch command {
	case Submit:
		return set(`state = $2, reason = NULL`, Submitted)
	case Reject:
		return set(`state = $2, reason = $3`, Draft, *in.Reason)
	case Cancel:
		return set(`state = $2`, Cancelled)
	case Approve:
		if e.MakerID == actor {
			return core.ErrApprover.New("the entry was made by " + actor + ": another approves it")
		}
		rule, roles, to := "fund_transfer_out", []string{"in_transit", "bank"}, InTransit
		switch e.Kind {
		case "charge":
			rule, roles, to = "bank_charge", []string{"expense", "bank"}, Posted
		case "interest":
			rule, roles, to = "bank_interest", []string{"bank", "income"}, Posted
		}
		f := fact(e, rule, e.EntryDate, roles, rate)
		if err := f.Publish(m, ctx, tx); err != nil {
			return err
		}
		if err := set(`state = $2, approver_id = $3, out_fact_id = $4`, to, actor, f.FactID); err != nil {
			return err
		}
		if e.StatementLineID == nil {
			return nil
		}
		// BR04: made from a statement line, it matches its line (unless the line was matched by hand meanwhile)
		if err := matching.Match(ctx, tx, *e.StatementLineID, "bank_entry", e.ID); err != nil && !errs.Is(err, core.ID, "NOT_MATCHABLE") {
			return err
		}
		return nil
	case Arrive:
		f := fact(e, "fund_transfer_in", *in.ArrivedOn, []string{"bank", "in_transit"}, rate)
		if err := f.Publish(m, ctx, tx); err != nil {
			return err
		}
		return set(`state = $2, arrived_on = $3, in_fact_id = $4`, Posted, *in.ArrivedOn, f.FactID)
	}
	return nil
}
