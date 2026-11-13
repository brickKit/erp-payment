//go:build integration

package itest

import (
	"encoding/json"
	"time"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/ids"
	"github.com/brickKit/be-sdk-go/v3/sectest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// policies: a legal entity's payment policy starts with no approver (version 0); an approver is a position or a
// principal, not both; set by version. Shanghai's approver is Bob, which the steps after rely on (PR03, BE02).
func (fx *fixture) policies() {
	t := fx.t
	path := "/api/payment/policies/" + fx.le
	assert.Equal(t, map[string]any{"legal_entity_id": fx.le, "approver_position_id": nil, "approver_principal": nil,
		"version": float64(0)}, fx.get(path))
	for _, bad := range []map[string]any{{"approver_principal": sectest.Bob, "approver_position_id": fx.d1},
		{"approver_position_id": "FIN-MGR"}, {"approver_principal": " "}} {
		code, out := fx.alice.do("PUT", path, bad)
		assert.Equal(t, 400, code, "%v: %v", bad, out)
	}
	code, out := fx.carol.do("GET", path, nil)
	assert.Equal(t, 404, code, out)
	code, out = fx.alice.do("PUT", path, map[string]any{"approver_principal": sectest.Bob, "version": 0})
	require.Equal(t, 200, code, out)
	assert.Equal(t, sectest.Bob, out["approver_principal"])
	assert.Equal(t, float64(1), out["version"])
	code, out = fx.alice.do("PUT", path, map[string]any{"approver_principal": sectest.Alice, "version": 0})
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "VERSION_CONFLICT", out["reason"])
}

// approvals: payment requests and entries are approved on the SDK's flow kernel (be-protocol chapter 17;
// docs/design.md PR02-PR05, PR07, BE02, BE04): no approver, nothing claimed (NO_APPROVER_CONFIGURED); the task by the shared
// task contract; the standard routes decide it (a rejection says why, the applicant's person and a stale version are
// refused, the same key replays); a rejection's claims released by the queued work; the release check before the
// release (NOT_RELEASED); cancelling withdraws, rejecting an approved request supersedes; a position without
// mdm/employee pauses the task; an entry approved on its task is posted.
func (fx *fixture) approvals() {
	t := fx.t
	path := "/api/payment/policies/" + fx.le
	policy := func(body map[string]any) {
		t.Helper()
		code, out := fx.alice.do("PUT", path, body)
		require.Equal(t, 200, code, out)
	}
	due := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	item := ids.New().String()
	fx.finance.item(item, "payable", fx.le, fx.supplier, "CNY", "1000.00", due)

	// PR02, BE02: no approver named: nothing is claimed, nothing changes
	policy(map[string]any{})
	r := fx.request(fx.alice, fx.reqBody("payment", fx.supplier, fx.cny, line(item, "100")))
	id := r["id"].(string)
	fx.reqRefused(fx.alice, id, "submit", nil, "NO_APPROVER_CONFIGURED")
	assert.Equal(t, "1000.00", fx.finance.available(item), "nothing claimed")
	assert.Equal(t, "draft", fx.get("/api/payment/requests/" + id)["state"])
	e := fx.entry(fx.alice, fx.entryBody("charge", fx.cny, "", "5"))
	fx.entryRefused(fx.alice, e["id"].(string), "submit", nil, "NO_APPROVER_CONFIGURED")
	policy(map[string]any{"approver_principal": sectest.Bob})

	// PR03: the task, Bob its candidate, no account number in its summary
	r = fx.reqAction(fx.alice, id, "submit", nil)
	claim := at(r, "lines", 0, "claim_id").(string)
	task := fx.task(id)
	assert.Equal(t, core.RequestTaskType, task["task_type"])
	assert.Equal(t, []any{sectest.Bob}, task["candidates"])
	assert.Equal(t, "100 CNY", at(task, "summary", "fields", 2, "value"))
	tid := task["id"].(string)
	decide := func(c client, task string, version any, decision string, comment any, key ...string) (int, map[string]any) {
		body := map[string]any{"version": version, "decision": decision}
		if comment != nil {
			body["comment"] = comment
		}
		return c.do("POST", "/api/payment/flow/tasks/"+task+"/decisions", body, key...)
	}
	code, out := decide(fx.bob, tid, task["version"], "reject", nil)
	assert.Equal(t, 400, code, out)
	assert.Equal(t, "REQUEST_INVALID", out["reason"])
	code, out = decide(fx.alice, tid, task["version"], "approve", nil)
	assert.Equal(t, 403, code, out)
	assert.Equal(t, "SELF_APPROVAL", out["reason"])
	code, out = decide(fx.bob, tid, task["version"].(float64)+1, "approve", nil)
	assert.Equal(t, 409, code, out)
	assert.Equal(t, "VERSION_CONFLICT", out["reason"])
	code, out = fx.carol.do("GET", "/api/payment/flow/tasks/"+tid, nil)
	assert.Equal(t, 404, code, out)

	// PR03: rejected on its task: back to draft with the comment; its claim released by the queued work
	code, out = decide(fx.bob, tid, task["version"], "reject", "金额不对")
	require.Equal(t, 200, code, out)
	back := fx.get("/api/payment/requests/" + id)
	assert.Equal(t, "draft", back["state"])
	assert.Equal(t, "金额不对", back["reason"])
	assert.Nil(t, at(back, "lines", 0, "claim_id"))
	require.Eventually(t, func() bool { return fx.finance.state(claim) == "released" }, 20*time.Second, 100*time.Millisecond,
		"the claim is released after the decision")

	// PR03, PR04: submitted again, approved on the standard route, the same key replays; released only as approved
	r = fx.reqAction(fx.alice, id, "submit", nil)
	task = fx.task(id)
	require.NotEqual(t, tid, task["id"])
	key := ids.New().String()
	code, first := decide(fx.bob, task["id"].(string), task["version"], "approve", nil, key)
	require.Equal(t, 200, code, first)
	code, again := decide(fx.bob, task["id"].(string), task["version"], "approve", nil, key)
	require.Equal(t, 200, code, again)
	assert.Equal(t, first["version"], again["version"])
	approved := fx.get("/api/payment/requests/" + id)
	assert.Equal(t, "approved", approved["state"])
	assert.Equal(t, sectest.Bob, approved["approver_id"])
	_, err := fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".payment_request SET amount = amount + 1 WHERE id = $1", id)
	require.NoError(t, err)
	fx.reqRefused(fx.bob, id, "release", nil, "NOT_RELEASED")
	_, err = fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".payment_request SET amount = amount - 1 WHERE id = $1", id)
	require.NoError(t, err)

	// PR03: an approved request rejected (not yet released): its approval superseded, back to draft
	fx.reqAction(fx.bob, id, "reject", map[string]any{"reason": "改付别的账户"})
	assert.Equal(t, "superseded", fx.instance(id))
	// PR07: cancelling a submitted request withdraws its approval
	fx.reqAction(fx.alice, id, "submit", nil)
	fx.reqAction(fx.alice, id, "cancel", nil)
	assert.Equal(t, "withdrawn", fx.instance(id))
	assert.Equal(t, "superseded", fx.task(id)["state"])

	// PR04, PR05: released and executed with the approval covering it; the release by the applicant refused
	r2 := fx.request(fx.alice, fx.reqBody("payment", fx.supplier, fx.cny, line(item, "50")))
	r2id := r2["id"].(string)
	fx.reqAction(fx.alice, r2id, "submit", nil)
	fx.reqAction(fx.bob, r2id, "approve", nil)
	fx.reqRefused(fx.alice, r2id, "release", nil, "APPROVER_IS_SUBMITTER")
	assert.Equal(t, "ready", fx.reqAction(fx.bob, r2id, "release", nil)["state"])
	_, err = fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".payment_request SET amount = amount + 1 WHERE id = $1", r2id)
	require.NoError(t, err)
	fx.reqRefused(fx.alice, r2id, "execute", map[string]any{"paid_amount": "50", "paid_on": fx.today(),
		"bank_reference": "A-1"}, "NOT_RELEASED")
	_, err = fx.admin.Exec(fx.ctx, "UPDATE "+itSchema+".payment_request SET amount = amount - 1 WHERE id = $1", r2id)
	require.NoError(t, err)
	assert.Equal(t, "paid", fx.reqAction(fx.alice, r2id, "execute", map[string]any{"paid_amount": "50", "paid_on": fx.today(),
		"bank_reference": "A-1"})["state"])

	// BE02: an entry approved on its task is posted by its decider with the rate taken when submitted
	eid := e["id"].(string)
	fx.entryAction(fx.alice, eid, "submit", nil)
	et := fx.task(eid)
	assert.Equal(t, core.EntryTaskType, et["task_type"])
	code, out = decide(fx.bob, et["id"].(string), et["version"], "approve", nil)
	require.Equal(t, 200, code, out)
	posted := fx.get("/api/payment/entries/" + eid)
	assert.Equal(t, "posted", posted["state"])
	assert.Equal(t, sectest.Bob, posted["approver_id"])
	var facts int
	require.NoError(t, fx.admin.QueryRow(fx.ctx, "SELECT count(*) FROM "+itSchema+".be_outbox WHERE type = $1 AND "+
		"convert_from(payload, 'UTF8')::jsonb->'source'->>'document_id' = $2", core.AccountingFact, eid).Scan(&facts))
	assert.Equal(t, 1, facts, "posted once, its bank charge fact")
	// BE04: cancelling a submitted entry withdraws its approval
	ce := fx.entry(fx.alice, fx.entryBody("charge", fx.cny, "", "7"))
	fx.entryAction(fx.alice, ce["id"].(string), "submit", nil)
	fx.entryAction(fx.alice, ce["id"].(string), "cancel", nil)
	assert.Equal(t, "withdrawn", fx.instance(ce["id"].(string)))

	// P17.3, P17.8: a position without mdm/employee in the deployment: the task waits, the manager sees why
	policy(map[string]any{"approver_position_id": ids.New().String()})
	p := fx.entry(fx.alice, fx.entryBody("charge", fx.cny, "", "6"))
	fx.entryAction(fx.alice, p["id"].(string), "submit", nil)
	pt := fx.task(p["id"].(string))
	assert.Equal(t, "paused", pt["state"])
	assert.Equal(t, "no_assignee", pt["paused_reason"])
	code, paused := fx.alice.do("GET", "/api/payment/flow/tasks?state=paused", nil)
	require.Equal(t, 200, code, paused)
	assert.NotEmpty(t, paused["tasks"])
	policy(map[string]any{"approver_principal": sectest.Bob})
}

// tasks are the approval tasks published for a document, newest version first (the outbox in the member's schema).
func (fx *fixture) tasks(document string) []map[string]any {
	fx.t.Helper()
	rows, err := fx.admin.Query(fx.ctx, "SELECT payload FROM "+itSchema+".be_outbox WHERE type = $1 "+
		"AND convert_from(payload, 'UTF8')::jsonb->'document'->>'id' = $2 ORDER BY ord DESC", core.TaskChanged, document)
	require.NoError(fx.t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var payload []byte
		require.NoError(fx.t, rows.Scan(&payload))
		var m map[string]any
		require.NoError(fx.t, json.Unmarshal(payload, &m))
		out = append(out, m)
	}
	require.NoError(fx.t, rows.Err())
	return out
}

// task is the latest state of the document's newest task.
func (fx *fixture) task(document string) map[string]any {
	fx.t.Helper()
	ts := fx.tasks(document)
	require.NotEmpty(fx.t, ts, "a task of %s", document)
	return ts[0]
}

// instance is the state of the document's newest approval instance.
func (fx *fixture) instance(document string) string {
	fx.t.Helper()
	var s string
	require.NoError(fx.t, fx.admin.QueryRow(fx.ctx, "SELECT state FROM "+itSchema+".be_flow_instance WHERE document_id = $1 "+
		"ORDER BY created_at DESC, id DESC LIMIT 1", document).Scan(&s))
	return s
}
