package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/bizdate"
	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/flow"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/work"
)

// The payment request's approval on the SDK's flow kernel (be-protocol chapter 17; docs/design.md PR02-PR08): the
// submission's transaction starts an instance, a decision on the task changes the request through FlowEnded,
// cancelling, a lapse and the rejection of an approved request withdraw or supersede it, and the release and the
// execution pass the release check.

// keyRevision is a request's key revision: it never changes once submitted (a rejection takes it back to draft, and
// submitting it again is a new instance), so one revision.
const keyRevision = 1

// Flow is the request's approval: one step, any of its candidates, the policy's position or principal; people always
// approve a payment; a rejection returns it to draft and says why.
func Flow() flow.Definition {
	return flow.Definition{Key: core.RequestFlow, Version: 1, TaskType: core.RequestTaskType, DocumentType: core.RequestDocument,
		Operation: core.OpRequestApprove, ReadOperation: core.OpRequestRead, OnReject: flow.Returned, RejectComment: true,
		Manual: true, Steps: []flow.Step{{Form: flow.Any, Rule: flow.PositionOrPerson}}}
}

// Document is the request as the kernel refers to it.
func Document(r Request) flow.Document {
	return flow.Document{Type: core.RequestDocument, ID: r.ID, Number: r.Number}
}

// basis is the digest of the request's key fields: the kind, the payee, the account, the currency, the amount and each
// line's open item and amount.
func basis(r Request) (string, error) {
	lines := make([]any, len(r.Lines))
	for i, l := range r.Lines {
		item := ""
		if l.OpenItemID != nil {
			item = *l.OpenItemID
		}
		lines[i] = map[string]any{"open_item_id": item, "amount": l.Amount}
	}
	return canon.Digest("erp.payment.request.approval_basis", 1, map[string]any{"kind": r.Kind, "payee_id": r.PayeeID,
		"bank_account_id": r.BankAccountID, "currency": r.Currency, "amount": r.Amount, "lines": lines})
}

// FlowDocument is the kernel's Document hook for a request: locked, its key revision, its legal entity's business date
// and its two dimensions.
func FlowDocument(ctx context.Context, tx db.Tx, d flow.Document) (int64, string, sdkauthz.Facts, error) {
	var tz, le string
	var path *string
	if err := tx.QueryRow(ctx, `SELECT timezone, legal_entity_id::text, department_path FROM payment_request
		WHERE id = $1::uuid FOR UPDATE`, d.ID).Scan(&tz, &le, &path); err != nil {
		return 0, "", nil, err
	}
	date, err := bizdate.Today(tz)
	return keyRevision, date, core.DocFacts(le, path), err
}

// JobReleaseClaims releases the claims of a request the kernel returned to draft: the decision's transaction does not
// call erp/finance, so the release is queued in it (PR03).
const JobReleaseClaims = "release-claims"

type releaseArgs struct {
	Claims []string `json:"claims"`
}

// ReleaseClaims is the queued release (idempotent at erp/finance); finance not reachable retries.
func ReleaseClaims(m *core.Base, ctx context.Context, w *work.Run) error {
	var a releaseArgs
	if err := json.Unmarshal(w.Args, &a); err != nil {
		return work.Permanent(err)
	}
	return releaseAll(m, ctx, a.Claims)
}

// FlowEnded is the kernel's end hook for a request (be-protocol P17.6): approved, the decider its approver; returned,
// back to draft with the decider's comment as the reason, its claims let go and their release queued.
func FlowEnded(m *core.Base, ctx context.Context, tx db.Tx, i flow.Instance) error {
	r, err := Get(ctx, tx, i.Document.ID, true)
	if err != nil {
		return err
	}
	if i.Ending == nil {
		return fmt.Errorf("requests: the approval of %s ended %s without a decision", r.ID, i.State)
	}
	command := Approve
	if i.State == flow.Returned {
		command = Reject
	}
	d := Lifecycle.Decide(r.State, command)
	if d.Effect != core.Move {
		return fmt.Errorf("requests: the approval of %s ended %s on a %s request", r.ID, i.State, r.State)
	}
	if command == Approve {
		if _, err := tx.Exec(ctx, `UPDATE payment_request SET state = $2, approver_id = $3, version = version + 1,
			updated_at = now() WHERE id = $1`, r.ID, Approved, i.Ending.By); err != nil {
			return err
		}
	} else {
		if claims := r.claims(); len(claims) > 0 {
			if _, err := m.RT.Work.Enqueue(ctx, tx, JobReleaseClaims, releaseArgs{Claims: claims}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_request_line SET claim_id = NULL, claim_expires_at = NULL
			WHERE request_id = $1`, r.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_request SET state = $2, reason = $3, version = version + 1,
			updated_at = now() WHERE id = $1`, r.ID, Draft, i.Ending.Comment); err != nil {
			return err
		}
	}
	_, err = changed(m, ctx, tx, i.Ending.By, d.Op, r.ID)
	return err
}

// startApproval starts the request's approval in the submission's transaction (PR02, PR03) for the approver the
// policy named before the claims were made.
func startApproval(m *core.Base, ctx context.Context, tx db.Tx, r Request, approver flow.Binding, actor string) error {
	digest, err := basis(r)
	if err != nil {
		return err
	}
	_, err = m.Flow.StartIn(ctx, tx, flow.Start{Flow: core.RequestFlow, Document: Document(r), LegalEntity: r.LegalEntityID,
		KeyRevision: keyRevision, Digests: []string{digest}, Bindings: []flow.Binding{approver}, Initiator: actor,
		Summary: flow.Summary{Title: "Payment request " + r.Number, Fields: []flow.SummaryField{
			{Key: "number", Label: "Number", Value: r.Number}, {Key: "kind", Label: "Kind", Value: r.Kind},
			{Key: "amount", Label: "Amount", Value: r.Amount + " " + r.Currency}}}})
	return err
}

// endApproval ends the approval of a request leaving its approval's way (PR03, PR07, PR08): a pending one withdrawn,
// an approved or released one superseded.
func endApproval(m *core.Base, ctx context.Context, tx db.Tx, r Request) error {
	switch r.State {
	case Submitted:
		return m.Flow.WithdrawIn(ctx, tx, Document(r))
	case Approved, Ready:
		return m.Flow.SupersedeIn(ctx, tx, Document(r))
	}
	return nil
}

// released is the release check (be-protocol P17.7) in the transactions that release and execute a request (PR04,
// PR05).
func released(m *core.Base, ctx context.Context, tx db.Tx, r Request) error {
	digest, err := basis(r)
	if err != nil {
		return err
	}
	err = m.Flow.ReleaseIn(ctx, tx, Document(r), keyRevision, []string{digest})
	var nr flow.ErrNotReleased
	if errors.As(err, &nr) {
		return core.ErrNotReleased.New(r.Number + ": " + nr.Why)
	}
	return err
}

// notApplicant is PR04's separation: the releaser is not the applicant's person (by account without mdm/employee).
func notApplicant(m *core.Base, ctx context.Context, tx db.Tx, r Request, actor string) error {
	if r.ApplicantID == nil {
		return nil
	}
	me, err := m.Flow.PersonOf(ctx, tx, actor)
	if err != nil {
		return err
	}
	applicant, err := m.Flow.PersonOf(ctx, tx, *r.ApplicantID)
	if err != nil {
		return err
	}
	if me == applicant || actor == *r.ApplicantID {
		return core.ErrApprover.New("the request was submitted by " + *r.ApplicantID + ": another releases it")
	}
	return nil
}

// decide is the approve route and the reject route of a submitted request (PR03): the caller's decision on the
// request's approval task through the kernel.
func decide(m *core.Base, c *httpx.Context, ctx context.Context, cur Request, command string, reason *string) error {
	choice := flow.Approve
	if command == Reject {
		choice = flow.Reject
	}
	subj, _ := sdkauthz.From(ctx)
	if _, err := m.Flow.DecideDocument(ctx, subj, c.Header(calls.HeaderIdempotencyKey), Document(cur), choice, reason); err != nil {
		return err
	}
	r, err := Read(m, ctx, cur.ID)
	if err != nil {
		return err
	}
	return c.JSON(200, r)
}
