package requests

import (
	"context"
	"fmt"

	core "github.com/brickKit/erp-payment/v3/internal/core"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/recovery"
)

// CheckHeldClaims is the recovery check of the released requests: each claim still confirmed at erp/finance.
const CheckHeldClaims = "erp.payment.held_claims"

// HeldClaims reports every released (ready) request one of whose claims erp/finance no longer holds confirmed
// (docs/design.md, "整套恢复之后"): the requests read in one read-only transaction, finance asked after it. Without
// erp/finance there is nothing to ask.
func HeldClaims(m *core.Base) func(context.Context, recovery.Read) (recovery.Result, error) {
	return func(ctx context.Context, read recovery.Read) (recovery.Result, error) {
		res := recovery.Result{Outcome: recovery.Pass}
		if !masterdata.Deployed(m, core.Finance) {
			return res, nil
		}
		type held struct{ request, number, claim string }
		var all []held
		err := read(ctx, func(ctx context.Context, tx db.Tx) error {
			rows, err := tx.Query(ctx, `SELECT r.id::text, r.number, l.claim_id::text FROM payment_request r
				JOIN payment_request_line l ON l.request_id = r.id WHERE r.state = 'ready' AND l.claim_id IS NOT NULL
				ORDER BY r.number, l.position`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var h held
				if err := rows.Scan(&h.request, &h.number, &h.claim); err != nil {
					return err
				}
				all = append(all, h)
			}
			return rows.Err()
		})
		if err != nil {
			return res, err
		}
		reported := map[string]bool{}
		for _, h := range all {
			state, err := masterdata.ClaimState(m, ctx, h.claim)
			if err != nil {
				return res, err
			}
			if state == "confirmed" || reported[h.request] {
				continue
			}
			reported[h.request] = true
			res.Differences = append(res.Differences, recovery.Difference{ObjectType: core.Requests, ObjectID: h.request,
				Detail: fmt.Sprintf("request %s is released but its claim %s is %q at erp/finance", h.number, h.claim, state)})
		}
		if len(res.Differences) > 0 {
			res.Outcome = recovery.Diff
			res.Disposal = "钱还没付的：取消这张申请（释放认领）后重新申请；钱已经付了的：照实登记执行，财务按事实核对（docs/design.md“整套恢复之后”）"
		}
		return res, nil
	}
}
