package payment

import (
	"context"
	"time"

	core "github.com/brickKit/erp-payment/v3/internal/core"
	orgunits "github.com/brickKit/erp-payment/v3/internal/orgunits"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/events"
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
	b *core.Base
}

// New builds the module from its runtime.
func New(rt *runtime.Runtime) (*Module, error) {
	b, err := core.NewBase(rt)
	if err != nil {
		return nil, err
	}
	return &Module{b: b}, nil
}

// Routes registers the user surface (contract/openapi.yaml).
func (m *Module) Routes(r *httpx.Routes) error {
	add := func(method, path, op string, h httpx.Handler) {
		r.Add(httpx.Route{Method: method, Path: path, Operation: op, Handle: h})
	}
	add("GET", "/api/payment/bank-accounts", core.OpBankAccountRead, core.Unsupported("listBankAccounts"))                           // listBankAccounts
	add("POST", "/api/payment/bank-accounts", core.OpBankAccountWrite, core.Unsupported("createBankAccount"))                        // createBankAccount
	add("GET", "/api/payment/bank-accounts/:id", core.OpBankAccountRead, core.Unsupported("getBankAccount"))                         // getBankAccount
	add("PATCH", "/api/payment/bank-accounts/:id", core.OpBankAccountWrite, core.Unsupported("updateBankAccount"))                   // updateBankAccount
	add("GET", "/api/payment/bank-accounts/:id/reconciliation", core.OpStatementRead, core.Unsupported("getReconciliation"))         // getReconciliation
	add("GET", "/api/payment/receipts", core.OpReceiptRead, core.Unsupported("listReceipts"))                                        // listReceipts
	add("POST", "/api/payment/receipts", core.OpReceiptRegister, core.Unsupported("registerReceipt"))                                // registerReceipt
	add("GET", "/api/payment/receipts/:id", core.OpReceiptRead, core.Unsupported("getReceipt"))                                      // getReceipt
	add("POST", "/api/payment/receipts/:id/reverse", core.OpReceiptReverse, core.Unsupported("reverseReceipt"))                      // reverseReceipt
	add("GET", "/api/payment/proposals", core.OpRequestRead, core.Unsupported("listProposals"))                                      // listProposals
	add("GET", "/api/payment/requests", core.OpRequestRead, core.Unsupported("listRequests"))                                        // listRequests
	add("POST", "/api/payment/requests", core.OpRequestEdit, core.Unsupported("createRequest"))                                      // createRequest
	add("GET", "/api/payment/requests/:id", core.OpRequestRead, core.Unsupported("getRequest"))                                      // getRequest
	add("PATCH", "/api/payment/requests/:id", core.OpRequestEdit, core.Unsupported("updateRequest"))                                 // updateRequest
	add("POST", "/api/payment/requests/:id/submit", core.OpRequestEdit, core.Unsupported("submitRequest"))                           // submitRequest
	add("POST", "/api/payment/requests/:id/approve", core.OpRequestApprove, core.Unsupported("approveRequest"))                      // approveRequest
	add("POST", "/api/payment/requests/:id/reject", core.OpRequestApprove, core.Unsupported("rejectRequest"))                        // rejectRequest
	add("POST", "/api/payment/requests/:id/release", core.OpRequestExecute, core.Unsupported("releaseRequest"))                      // releaseRequest
	add("POST", "/api/payment/requests/:id/execute", core.OpRequestExecute, core.Unsupported("executeRequest"))                      // executeRequest
	add("POST", "/api/payment/requests/:id/return", core.OpRequestExecute, core.Unsupported("returnRequest"))                        // returnRequest
	add("POST", "/api/payment/requests/:id/cancel", core.OpRequestEdit, core.Unsupported("cancelRequest"))                           // cancelRequest
	add("GET", "/api/payment/entries", core.OpEntryRead, core.Unsupported("listEntries"))                                            // listEntries
	add("POST", "/api/payment/entries", core.OpEntryEdit, core.Unsupported("createEntry"))                                           // createEntry
	add("GET", "/api/payment/entries/:id", core.OpEntryRead, core.Unsupported("getEntry"))                                           // getEntry
	add("PATCH", "/api/payment/entries/:id", core.OpEntryEdit, core.Unsupported("updateEntry"))                                      // updateEntry
	add("POST", "/api/payment/entries/:id/submit", core.OpEntryEdit, core.Unsupported("submitEntry"))                                // submitEntry
	add("POST", "/api/payment/entries/:id/approve", core.OpEntryApprove, core.Unsupported("approveEntry"))                           // approveEntry
	add("POST", "/api/payment/entries/:id/reject", core.OpEntryApprove, core.Unsupported("rejectEntry"))                             // rejectEntry
	add("POST", "/api/payment/entries/:id/arrive", core.OpEntryEdit, core.Unsupported("arriveEntry"))                                // arriveEntry
	add("POST", "/api/payment/entries/:id/cancel", core.OpEntryEdit, core.Unsupported("cancelEntry"))                                // cancelEntry
	add("GET", "/api/payment/statement-lines", core.OpStatementRead, core.Unsupported("listStatementLines"))                         // listStatementLines
	add("POST", "/api/payment/statement-lines", core.OpReconcile, core.Unsupported("registerStatementLines"))                        // registerStatementLines
	add("POST", "/api/payment/statement-lines/:id/match", core.OpReconcile, core.Unsupported("matchStatementLine"))                  // matchStatementLine
	add("POST", "/api/payment/statement-lines/:id/unmatch", core.OpReconcile, core.Unsupported("unmatchStatementLine"))              // unmatchStatementLine
	add("POST", "/api/payment/statement-lines/:id/resolve", core.OpReconcile, core.Unsupported("resolveStatementLine"))              // resolveStatementLine
	add("POST", "/api/payment/statement-lines/:id/reopen", core.OpReconcile, core.Unsupported("reopenStatementLine"))                // reopenStatementLine
	add("POST", "/api/payment/statement-lines/:id/create-entry", core.OpReconcile, core.Unsupported("createEntryFromStatementLine")) // createEntryFromStatementLine
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
	return nil
}

// job and received adapt a module's function, which takes the shared base first, to what the SDK registers.
func (m *Module) job(f func(*core.Base, context.Context, *work.Run) error) func(context.Context, *work.Run) error {
	return func(ctx context.Context, r *work.Run) error { return f(m.b, ctx, r) }
}

func (m *Module) received(f func(*core.Base, context.Context, db.Tx, events.Received) error) func(context.Context, db.Tx, events.Received) error {
	return func(ctx context.Context, tx db.Tx, e events.Received) error { return f(m.b, ctx, tx, e) }
}
