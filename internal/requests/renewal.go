package requests

import (
	"context"
	"encoding/json"
	"time"

	core "github.com/brickKit/erp-payment/v3/internal/core"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/work"
)

// JobRenewClaim renews a waiting request's claim before its end (PR08).
const JobRenewClaim = "renew-claim"

// LapseReason is the reason a lapsed request goes back to draft with.
const LapseReason = "付款认领已失效"

type renewArgs struct {
	RequestID string `json:"request_id"`
	ClaimID   string `json:"claim_id"`
}

// renewAt is when a claim ending at end is renewed: RenewAhead before it, or halfway there for a shorter period.
func renewAt(end time.Time) time.Time {
	ahead := core.RenewAhead
	if half := time.Until(end) / 2; half < ahead {
		ahead = half
	}
	return end.Add(-ahead)
}

// scheduleRenewal queues the renewal of a claim ending at end (RFC 3339), in the transaction that holds the claim.
func scheduleRenewal(m *core.Base, ctx context.Context, tx db.Tx, request, claim, end string) error {
	at, err := time.Parse(time.RFC3339Nano, end)
	if err != nil {
		return err
	}
	_, err = m.RT.Work.EnqueueAt(ctx, tx, JobRenewClaim, renewArgs{RequestID: request, ClaimID: claim}, renewAt(at))
	return err
}

// RenewClaim is PR08: a waiting request's claim renewed at erp/finance and its next renewal queued; a renewal
// refused takes the request back to draft with the reason, its other claims released. A claim no longer the
// request's (released, confirmed, a new submission) is left alone.
func RenewClaim(m *core.Base, ctx context.Context, w *work.Run) error {
	var a renewArgs
	if err := json.Unmarshal(w.Args, &a); err != nil {
		return work.Permanent(err)
	}
	r, err := Read(m, ctx, a.RequestID)
	if err != nil {
		return work.Permanent(err)
	}
	if !holds(r, a.ClaimID) {
		return nil
	}
	end, err := masterdata.Renew(m, ctx, a.ClaimID)
	if err != nil {
		if !core.ClaimRefused(err) {
			return err // finance not reachable: the work item retries
		}
		return lapse(m, ctx, r, a.ClaimID)
	}
	return m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		cur, err := Get(ctx, tx, a.RequestID, true)
		if err != nil || !holds(cur, a.ClaimID) {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_request_line SET claim_expires_at = $2::timestamptz WHERE claim_id = $1`,
			a.ClaimID, end); err != nil {
			return err
		}
		return scheduleRenewal(m, ctx, tx, a.RequestID, a.ClaimID, end)
	})
}

// holds says whether a request waiting for approval or its release holds the claim.
func holds(r Request, claim string) bool {
	if r.State != Submitted && r.State != Approved {
		return false
	}
	for _, c := range r.claims() {
		if c == claim {
			return true
		}
	}
	return false
}

// lapse takes the request back to draft with the reason; its other claims released first.
func lapse(m *core.Base, ctx context.Context, r Request, lost string) error {
	var others []string
	for _, c := range r.claims() {
		if c != lost {
			others = append(others, c)
		}
	}
	if err := releaseAll(m, ctx, others); err != nil {
		return err
	}
	reason := LapseReason
	_, _, err := moved(m, ctx, r.ID, Lapse, "", func(tx db.Tx, cur Request) error {
		if _, err := tx.Exec(ctx, `UPDATE payment_request_line SET claim_id = NULL, claim_expires_at = NULL
			WHERE request_id = $1`, cur.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE payment_request SET state = $2, reason = $3, version = version + 1, updated_at = now()
			WHERE id = $1`, cur.ID, Draft, reason)
		return err
	})
	return err
}
