package entries

import (
	"context"
	"encoding/json"
	"fmt"

	core "github.com/brickKit/erp-payment/v3/internal/core"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/bizdate"
	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/flow"
	"github.com/brickKit/be-sdk-go/v3/httpx"
)

// The entry's approval on the SDK's flow kernel (be-protocol chapter 17; docs/design.md BE02): submitting takes the
// exchange rate and starts an instance, a decision on the task posts the entry or returns it to draft through
// FlowEnded, cancelling a submitted entry withdraws it.

// keyRevision is an entry's key revision: it does not change once submitted.
const keyRevision = 1

// Flow is the entry's approval: one step, any of its candidates, the policy's position or principal; people always
// approve; a rejection returns it to draft and says why.
func Flow() flow.Definition {
	return flow.Definition{Key: core.EntryFlow, Version: 1, TaskType: core.EntryTaskType, DocumentType: core.EntryDocument,
		Operation: core.OpEntryApprove, ReadOperation: core.OpEntryRead, OnReject: flow.Returned, RejectComment: true,
		Manual: true, Steps: []flow.Step{{Form: flow.Any, Rule: flow.PositionOrPerson}}}
}

// Document is the entry as the kernel refers to it.
func Document(e Entry) flow.Document {
	return flow.Document{Type: core.EntryDocument, ID: e.ID, Number: e.Number}
}

// basis is the digest of the entry's key fields: the kind, the accounts, the currency, the amount and the date.
func basis(e Entry) (string, error) {
	to := ""
	if e.ToBankAccountID != nil {
		to = *e.ToBankAccountID
	}
	return canon.Digest("erp.payment.entry.approval_basis", 1, map[string]any{"kind": e.Kind, "bank_account_id": e.BankAccountID,
		"to_bank_account_id": to, "currency": e.Currency, "amount": e.Amount, "entry_date": e.EntryDate})
}

// FlowDocument is the kernel's Document hook for an entry: locked, its key revision, its legal entity's business date
// and its dimension.
func FlowDocument(ctx context.Context, tx db.Tx, d flow.Document) (int64, string, sdkauthz.Facts, error) {
	var tz, le string
	if err := tx.QueryRow(ctx, `SELECT timezone, legal_entity_id::text FROM bank_entry WHERE id = $1::uuid FOR UPDATE`,
		d.ID).Scan(&tz, &le); err != nil {
		return 0, "", nil, err
	}
	date, err := bizdate.Today(tz)
	return keyRevision, date, core.EntityFacts(le), err
}

// FlowEnded is the kernel's end hook for an entry (be-protocol P17.6): approved, posted by its decider with the rate
// taken when it was submitted (the posting is in the decision's transaction, which calls no one); returned, back to
// draft with the decider's comment as the reason.
func FlowEnded(m *core.Base, ctx context.Context, tx db.Tx, i flow.Instance) error {
	e, err := Get(ctx, tx, i.Document.ID, true)
	if err != nil {
		return err
	}
	if i.Ending == nil {
		return fmt.Errorf("entries: the approval of %s ended %s without a decision", e.ID, i.State)
	}
	command, in := Approve, actionIn{Reason: i.Ending.Comment}
	if i.State == flow.Returned {
		command = Reject
	}
	d := Lifecycle.Decide(e.State, command)
	if d.Effect != core.Move {
		return fmt.Errorf("entries: the approval of %s ended %s on a %s entry", e.ID, i.State, e.State)
	}
	var rate *masterdata.Rate
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT rate FROM bank_entry WHERE id = $1`, e.ID).Scan(&raw); err != nil {
		return err
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &rate); err != nil {
			return err
		}
	}
	if err := apply(m, ctx, tx, e, command, i.Ending.By, in, rate); err != nil {
		return err
	}
	_, err = changed(m, ctx, tx, i.Ending.By, d.Op, e.ID)
	return err
}

// startApproval starts the entry's approval in the submission's transaction (BE02), the rate taken before it kept on
// the entry for the posting.
func startApproval(m *core.Base, ctx context.Context, tx db.Tx, e Entry, approver flow.Binding, rate *masterdata.Rate, actor string) error {
	if _, err := tx.Exec(ctx, `UPDATE bank_entry SET rate = $2 WHERE id = $1`, e.ID, rate); err != nil {
		return err
	}
	digest, err := basis(e)
	if err != nil {
		return err
	}
	_, err = m.Flow.StartIn(ctx, tx, flow.Start{Flow: core.EntryFlow, Document: Document(e), LegalEntity: e.LegalEntityID,
		KeyRevision: keyRevision, Digests: []string{digest}, Bindings: []flow.Binding{approver}, Initiator: actor,
		Summary: flow.Summary{Title: "Bank entry " + e.Number, Fields: []flow.SummaryField{
			{Key: "number", Label: "Number", Value: e.Number}, {Key: "kind", Label: "Kind", Value: e.Kind},
			{Key: "amount", Label: "Amount", Value: e.Amount + " " + e.Currency}}}})
	return err
}

// decide is the approve and reject routes (BE02): the caller's decision on the entry's approval task through the
// kernel.
func decide(m *core.Base, c *httpx.Context, ctx context.Context, cur Entry, command string, reason *string) error {
	choice := flow.Approve
	if command == Reject {
		choice = flow.Reject
	}
	subj, _ := sdkauthz.From(ctx)
	if _, err := m.Flow.DecideDocument(ctx, subj, c.Header(calls.HeaderIdempotencyKey), Document(cur), choice, reason); err != nil {
		return err
	}
	e, err := read(m, ctx, cur.ID)
	if err != nil {
		return err
	}
	return c.JSON(200, e)
}
