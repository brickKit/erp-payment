package payment

import (
	"context"
	"time"

	bankaccounts "github.com/brickKit/erp-payment/v3/internal/bankaccounts"
	core "github.com/brickKit/erp-payment/v3/internal/core"
	entries "github.com/brickKit/erp-payment/v3/internal/entries"
	orgunits "github.com/brickKit/erp-payment/v3/internal/orgunits"
	policies "github.com/brickKit/erp-payment/v3/internal/policies"
	receipts "github.com/brickKit/erp-payment/v3/internal/receipts"
	requests "github.com/brickKit/erp-payment/v3/internal/requests"
	statements "github.com/brickKit/erp-payment/v3/internal/statements"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/flow"
	"github.com/brickKit/be-sdk-go/v3/grpcx"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/runtime"
	"github.com/brickKit/be-sdk-go/v3/work"

	paymentv3 "github.com/brickKit/erp-payment/contract/v3/gen/go/be/erp/payment/v3"
)

// Module is the payment component as the launcher and the shells run it: it only assembles the business modules
// under internal/ (their map is internal/AGENTS.md).
type Module struct {
	paymentv3.UnimplementedFactServiceServer
	b         *core.Base
	employees *flow.Employees // nil without mdm/employee
}

// New builds the module from its runtime: the flow kernel of the payment requests' and entries' approvals (be-protocol
// chapter 17), its hooks the two modules', its directory the employee projection when the deployment has mdm/employee.
func New(rt *runtime.Runtime) (*Module, error) {
	b, err := core.NewBase(rt)
	if err != nil {
		return nil, err
	}
	m := &Module{b: b}
	k := &flow.Kernel{Component: core.ID, Stream: core.Stream, TaskEvent: core.TaskChanged, HeartbeatEvent: core.TaskHeartbeat,
		Operation: core.OpFlowDecide, ManageOperation: core.OpFlowManage,
		Definitions: []flow.Definition{requests.Flow(), entries.Flow()},
		Hooks: flow.Hooks{
			Document: func(ctx context.Context, tx db.Tx, d flow.Document) (int64, string, sdkauthz.Facts, error) {
				if d.Type == core.EntryDocument {
					return entries.FlowDocument(ctx, tx, d)
				}
				return requests.FlowDocument(ctx, tx, d)
			},
			Ended: func(ctx context.Context, tx db.Tx, i flow.Instance) error {
				if i.Document.Type == core.EntryDocument {
					return entries.FlowEnded(m.b, ctx, tx, i)
				}
				return requests.FlowEnded(m.b, ctx, tx, i)
			}},
		DB: rt.DB, Log: rt.Logger}
	if rt.Events != nil {
		k.Events = rt.Events
	}
	if rt.Audit != nil {
		k.Audit = rt.Audit
	}
	if e, ok := flow.NewEmployees(rt); ok {
		m.employees, k.Directory = e, e
	}
	if err := k.Check(); err != nil {
		return nil, err
	}
	b.Flow = k
	return m, nil
}

// FlowKernel gives the host the kernel, whose standard routes it registers after the module's (be-protocol P17.14).
func (m *Module) FlowKernel() *flow.Kernel { return m.b.Flow }

// Start runs the employee projection the approvals resolve against (be-protocol P17.8).
func (m *Module) Start(ctx context.Context) error {
	if m.employees == nil {
		return nil
	}
	return m.employees.Start(ctx)
}

// Routes registers the user surface (contract/openapi.yaml).
func (m *Module) Routes(r *httpx.Routes) error {
	add := func(method, path, op string, h httpx.Handler) {
		r.Add(httpx.Route{Method: method, Path: path, Operation: op, Handle: h})
	}
	h := func(f func(*core.Base, *httpx.Context) error) httpx.Handler {
		return func(c *httpx.Context) error { return f(m.b, c) }
	}
	add("GET", "/api/payment/policies/:legal_entity_id", core.OpPolicyRead, h(policies.GetPolicy))                        // getPaymentPolicy
	add("PUT", "/api/payment/policies/:legal_entity_id", core.OpPolicyManage, h(policies.PutPolicy))                      // putPaymentPolicy
	add("GET", "/api/payment/bank-accounts", core.OpBankAccountRead, h(bankaccounts.ListBankAccounts))                    // listBankAccounts
	add("POST", "/api/payment/bank-accounts", core.OpBankAccountWrite, h(bankaccounts.CreateBankAccount))                 // createBankAccount
	add("GET", "/api/payment/bank-accounts/:id", core.OpBankAccountRead, h(bankaccounts.GetBankAccount))                  // getBankAccount
	add("PATCH", "/api/payment/bank-accounts/:id", core.OpBankAccountWrite, h(bankaccounts.UpdateBankAccount))            // updateBankAccount
	add("GET", "/api/payment/bank-accounts/:id/reconciliation", core.OpStatementRead, h(statements.Reconciliation))       // getReconciliation
	add("GET", "/api/payment/receipts", core.OpReceiptRead, h(receipts.ListReceipts))                                     // listReceipts
	add("POST", "/api/payment/receipts", core.OpReceiptRegister, h(receipts.RegisterReceipt))                             // registerReceipt
	add("GET", "/api/payment/receipts/:id", core.OpReceiptRead, h(receipts.GetReceipt))                                   // getReceipt
	add("POST", "/api/payment/receipts/:id/reverse", core.OpReceiptReverse, h(receipts.ReverseReceipt))                   // reverseReceipt
	add("GET", "/api/payment/proposals", core.OpRequestRead, h(requests.ListProposals))                                   // listProposals
	add("GET", "/api/payment/requests", core.OpRequestRead, h(requests.ListRequests))                                     // listRequests
	add("POST", "/api/payment/requests", core.OpRequestEdit, h(requests.CreateRequest))                                   // createRequest
	add("GET", "/api/payment/requests/:id", core.OpRequestRead, h(requests.GetRequest))                                   // getRequest
	add("PATCH", "/api/payment/requests/:id", core.OpRequestEdit, h(requests.UpdateRequest))                              // updateRequest
	add("POST", "/api/payment/requests/:id/submit", core.OpRequestEdit, requests.Action(m.b, requests.Submit))            // submitRequest
	add("POST", "/api/payment/requests/:id/approve", core.OpRequestApprove, requests.Action(m.b, requests.Approve))       // approveRequest
	add("POST", "/api/payment/requests/:id/reject", core.OpRequestApprove, requests.Action(m.b, requests.Reject))         // rejectRequest
	add("POST", "/api/payment/requests/:id/release", core.OpRequestExecute, requests.Action(m.b, requests.Release))       // releaseRequest
	add("POST", "/api/payment/requests/:id/execute", core.OpRequestExecute, requests.Action(m.b, requests.Execute))       // executeRequest
	add("POST", "/api/payment/requests/:id/return", core.OpRequestExecute, requests.Action(m.b, requests.Return))         // returnRequest
	add("POST", "/api/payment/requests/:id/cancel", core.OpRequestEdit, requests.Action(m.b, requests.Cancel))            // cancelRequest
	add("GET", "/api/payment/entries", core.OpEntryRead, h(entries.ListEntries))                                          // listEntries
	add("POST", "/api/payment/entries", core.OpEntryEdit, h(entries.CreateEntry))                                         // createEntry
	add("GET", "/api/payment/entries/:id", core.OpEntryRead, h(entries.GetEntry))                                         // getEntry
	add("PATCH", "/api/payment/entries/:id", core.OpEntryEdit, h(entries.UpdateEntry))                                    // updateEntry
	add("POST", "/api/payment/entries/:id/submit", core.OpEntryEdit, entries.Action(m.b, entries.Submit))                 // submitEntry
	add("POST", "/api/payment/entries/:id/approve", core.OpEntryApprove, entries.Action(m.b, entries.Approve))            // approveEntry
	add("POST", "/api/payment/entries/:id/reject", core.OpEntryApprove, entries.Action(m.b, entries.Reject))              // rejectEntry
	add("POST", "/api/payment/entries/:id/arrive", core.OpEntryEdit, entries.Action(m.b, entries.Arrive))                 // arriveEntry
	add("POST", "/api/payment/entries/:id/cancel", core.OpEntryEdit, entries.Action(m.b, entries.Cancel))                 // cancelEntry
	add("GET", "/api/payment/statement-lines", core.OpStatementRead, h(statements.ListLines))                             // listStatementLines
	add("POST", "/api/payment/statement-lines", core.OpReconcile, h(statements.RegisterLines))                            // registerStatementLines
	add("POST", "/api/payment/statement-lines/:id/match", core.OpReconcile, statements.Action(m.b, statements.Match))     // matchStatementLine
	add("POST", "/api/payment/statement-lines/:id/unmatch", core.OpReconcile, statements.Action(m.b, statements.Unmatch)) // unmatchStatementLine
	add("POST", "/api/payment/statement-lines/:id/resolve", core.OpReconcile, statements.Action(m.b, statements.Resolve)) // resolveStatementLine
	add("POST", "/api/payment/statement-lines/:id/reopen", core.OpReconcile, statements.Action(m.b, statements.Reopen))   // reopenStatementLine
	add("POST", "/api/payment/statement-lines/:id/create-entry", core.OpReconcile, h(statements.CreateEntryFromLine))     // createEntryFromStatementLine
	return nil
}

// Services registers the system surface: the facts by identity for any component.
func (m *Module) Services(s *grpcx.Services) error {
	s.Register(&paymentv3.FactService_ServiceDesc, m, map[string]grpcx.Method{
		"Read": {Operation: core.OpSysRead, Callers: []string{grpcx.AnyComponent}}})
	return nil
}

// Subscriptions declares the department projection's input (protocol 02).
func (m *Module) Subscriptions(s *events.Subscriptions) error {
	s.Add(events.Sub{Name: "org-unit-events", Streams: []string{core.OrgStream}, Types: []string{core.OrgUnitEvents},
		Handle: m.received(orgunits.OnOrgUnit)})
	if m.employees != nil {
		s.Add(m.employees.Sub())
	}
	return nil
}

// Jobs declares the background work (protocol 01).
func (m *Module) Jobs(j *work.Jobs) error {
	j.Add(work.Job{Name: "align-org-units", Kind: work.Singleton, Effect: work.Local, Interval: time.Hour,
		MaxAttempts: work.Unlimited, Handle: m.job(orgunits.AlignOrgUnits)})
	j.Add(work.Job{Name: orgunits.JobRepairOrgUnits, Kind: work.Queue, Effect: work.IdempotentExternal,
		MaxAttempts: work.Unlimited, Handle: m.job(orgunits.RepairOrgUnits)})
	j.Add(work.Job{Name: orgunits.JobRealignOrgUnits, Kind: work.Queue, Effect: work.IdempotentExternal,
		MaxAttempts: work.Unlimited, Handle: m.job(orgunits.RealignOrgUnits)})
	j.Add(work.Job{Name: requests.JobRenewClaim, Kind: work.Queue, Effect: work.IdempotentExternal,
		MaxAttempts: work.Unlimited, Handle: m.job(requests.RenewClaim)})
	j.Add(work.Job{Name: requests.JobReleaseClaims, Kind: work.Queue, Effect: work.IdempotentExternal,
		MaxAttempts: work.Unlimited, Handle: m.job(requests.ReleaseClaims)})
	m.b.Flow.Jobs(j) // paused approval tasks resolved again; the heartbeat
	return nil
}

// job and received adapt a module's function, which takes the shared base first, to what the SDK registers.
func (m *Module) job(f func(*core.Base, context.Context, *work.Run) error) func(context.Context, *work.Run) error {
	return func(ctx context.Context, r *work.Run) error { return f(m.b, ctx, r) }
}

func (m *Module) received(f func(*core.Base, context.Context, db.Tx, events.Received) error) func(context.Context, db.Tx, events.Received) error {
	return func(ctx context.Context, tx db.Tx, e events.Received) error { return f(m.b, ctx, tx, e) }
}
