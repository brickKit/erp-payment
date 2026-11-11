//go:build integration

package itest

import (
	"strconv"
	"time"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/recoverypb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// recoveryChecks: a full recovery asks this member, as the recovery tool only, whether the objects its audit names are
// still there at their versions (be-protocol chapter 14, P14.3), each type it audits, and runs its check: the
// released requests whose claims erp/finance no longer holds confirmed (docs/design.md, "整套恢复之后").
func (fx *fixture) recoveryChecks() {
	t := fx.t
	dial := func(as string) *grpc.ClientConn {
		conn, err := grpc.NewClient(itSystem, grpc.WithTransportCredentials(credentials.NewTLS(fx.w.TLS(t, as).ClientConfig(core.ID))))
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	_, err := recoverypb.NewCheckServiceClient(dial(core.Finance)).Check(fx.ctx, &recoverypb.CheckRequest{Recovery: "r"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "the recovery tool only: %v", err)
	cs := recoverypb.NewCheckServiceClient(dial("ops/recover"))
	for _, typ := range []string{core.BankAccounts, core.Receipts, core.Requests, core.Entries, core.Statements} {
		rows, err := fx.admin.Query(fx.ctx, "SELECT DISTINCT ON (target->>'id') target->>'id', target->>'version' FROM "+
			itSchema+".be_audit_log WHERE target->>'type' = $1 ORDER BY target->>'id', ord DESC", typ)
		require.NoError(t, err)
		var refs []*recoverypb.ObjectRef
		for rows.Next() {
			var id, version string
			require.NoError(t, rows.Scan(&id, &version))
			refs = append(refs, &recoverypb.ObjectRef{Type: typ, Id: id, Version: version})
		}
		rows.Close()
		require.NotEmpty(t, refs, "%s is audited", typ)
		present, err := cs.Present(fx.ctx, &recoverypb.PresentRequest{Recovery: "r", Objects: refs})
		require.NoError(t, err, typ)
		for i, a := range present.GetAnswers() {
			assert.Equal(t, "present", a.GetAnswer(), "%s %s", typ, refs[i].GetId())
		}
		last := refs[0]
		v, err := strconv.ParseInt(last.GetVersion(), 10, 64)
		require.NoError(t, err, "%s %s has a version", typ, last.GetId())
		present, err = cs.Present(fx.ctx, &recoverypb.PresentRequest{Recovery: "r", Objects: []*recoverypb.ObjectRef{
			{Type: typ, Id: last.GetId(), Version: strconv.FormatInt(v+1000, 10)}, {Type: typ, Id: ids.New().String(), Version: "1"}}})
		require.NoError(t, err, typ)
		assert.Equal(t, "older", present.GetAnswers()[0].GetAnswer(), typ)
		assert.Equal(t, "absent", present.GetAnswers()[1].GetAnswer(), typ)
	}

	// the check: of two released requests, the one whose claim finance released is reported
	item := ids.New().String()
	fx.finance.item(item, "payable", fx.le, fx.supplier, "CNY", "100.00", time.Now().Format("2006-01-02"))
	ready := func(amount string) map[string]any {
		r := fx.request(fx.alice, fx.reqBody("payment", fx.supplier, fx.cny, line(item, amount)))
		fx.reqAction(fx.alice, r["id"].(string), "submit", nil)
		fx.reqAction(fx.bob, r["id"].(string), "approve", nil)
		return fx.reqAction(fx.bob, r["id"].(string), "release", nil)
	}
	held, dropped := ready("30"), ready("40")
	fx.finance.drop(at(dropped, "lines", 0, "claim_id").(string))
	res, err := cs.Check(fx.ctx, &recoverypb.CheckRequest{Recovery: "r"})
	require.NoError(t, err)
	var claims *recoverypb.CheckResult
	for _, r := range res.GetResults() {
		if r.GetCheck() == "erp.payment.held_claims" {
			claims = r
		}
	}
	require.NotNil(t, claims, "the check is registered")
	assert.Equal(t, "diff", claims.GetOutcome())
	require.Len(t, claims.GetDifferences(), 1, claims.GetDifferences())
	assert.Equal(t, dropped["id"], claims.GetDifferences()[0].GetObjectId())
	assert.NotEqual(t, held["id"], claims.GetDifferences()[0].GetObjectId())
	assert.NotEmpty(t, claims.GetDisposal())
}
