package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"github.com/brickKit/be-sdk-go/v3/audit"
	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/brickKit/be-sdk-go/v3/idem"
	"github.com/brickKit/be-sdk-go/v3/identity"
	"github.com/brickKit/be-sdk-go/v3/money"
	apd "github.com/cockroachdb/apd/v3"
)

// The component's own reasons (the SDK's catalog has the shared ones).
var (
	ErrVersion = errs.Define(ID, "VERSION_CONFLICT", errs.Aborted, 409, errs.RetryNo, "The record changed since that version")
	ErrState   = errs.Define(ID, "STATE_CONFLICT", errs.FailedPrecondition, 409, errs.RetryNo,
		"The record's state does not allow this")
	ErrLegalEntity = errs.Define(ID, "LEGAL_ENTITY_NOT_USABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The legal entity does not exist or is not active")
	ErrDepartment = errs.Define(ID, "DEPARTMENT_NOT_USABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The department does not exist, is retired or belongs to another legal entity")
	ErrCurrency = errs.Define(ID, "CURRENCY_NOT_USABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The currency does not exist or is not active")
	ErrPartner = errs.Define(ID, "PARTNER_NOT_USABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The partner does not exist or is not active")
	ErrCode = errs.Define(ID, "CODE_TAKEN", errs.AlreadyExists, 409, errs.RetryNo, "The code is taken")
	// RC01: a bank reference registered on the account already (receipts not reversed)
	ErrBankReference = errs.Define(ID, "BANK_REFERENCE_TAKEN", errs.AlreadyExists, 409, errs.RetryNo,
		"The bank reference is registered on this account already")
	ErrBankAccount = errs.Define(ID, "BANK_ACCOUNT_NOT_USABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The bank account does not exist, is inactive, belongs to another legal entity or holds another currency")
	ErrOrder = errs.Define(ID, "ORDER_NOT_USABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The sales order does not exist or is not the payer's in this legal entity")
	// PR02: erp/finance refused a claim; its reasons, answered as they came (protocol 05's claim pattern)
	ErrClaimExceeds = errs.Define(ID, "CLAIM_EXCEEDS_AVAILABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"More than the open item's available balance")
	ErrClaimConflict = errs.Define(ID, "CLAIM_CONFLICT", errs.FailedPrecondition, 409, errs.RetryNo,
		"The claim was made before with another amount")
	ErrClaimState = errs.Define(ID, "CLAIM_STATE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The open item cannot be claimed for this payment")
	// PR02: an open item that is not the payee's to be paid in this kind and currency (erp/finance's due items)
	ErrOpenItem = errs.Define(ID, "OPEN_ITEM_NOT_PAYABLE", errs.FailedPrecondition, 409, errs.RetryNo,
		"The open item is not one the payee is paid against in this kind and currency")
	// PR04: a claim lapsed or was released before the request was released for payment
	ErrClaimLost = errs.Define(ID, "CLAIM_LOST", errs.FailedPrecondition, 409, errs.RetryNo,
		"A payment claim of the request is no longer held: submit the request again")
	// PR03, PR04, BE02: an approver or releaser is not the one who submitted or made it (by account)
	ErrApprover = errs.Define(ID, "APPROVER_IS_SUBMITTER", errs.FailedPrecondition, 409, errs.RetryNo,
		"The one who submitted or made it does not approve or release it")
)

var (
	UUIDForm     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	CurrencyForm = regexp.MustCompile(`^[A-Z]{3}$`)
	DateForm     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	DecimalForm  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

// DocFacts are the facts of a payment request's two dimensions; one without a department has none
// (security 07: no department never matches a tree, it never widens to the whole organization).
func DocFacts(legalEntity string, departmentPath *string) sdkauthz.Facts {
	dept := sdkauthz.Null
	if departmentPath != nil {
		dept = sdkauthz.Value(*departmentPath)
	}
	return sdkauthz.Facts{DimLegalEntity: sdkauthz.Value(legalEntity), DimDepartment: dept}
}

// EntityFacts are the facts of a record with the legal entity dimension only.
func EntityFacts(legalEntity string) sdkauthz.Facts {
	return sdkauthz.Facts{DimLegalEntity: sdkauthz.Value(legalEntity)}
}

// Number takes a document number of a series that allows gaps: <prefix>-<legal entity code>-<year>-<six digits>
// (prefixes RC, PR, BE).
func Number(m *Base, ctx context.Context, prefix, legalEntity, code, date string) (string, error) {
	year := date[:4]
	n, err := m.RT.DB.NextGappedNumber(ctx, db.Series{LegalEntity: legalEntity, Code: prefix, Period: year})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s-%06d", prefix, code, year, n), nil
}

// BindOptional decodes a body the contract makes optional (Versioned, Reason); no body leaves v as it is.
func BindOptional(c *httpx.Context, v any) error {
	body, err := c.Body()
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return err
	}
	return c.BindJSON(v)
}

// IDResult is what an idempotent create keeps (the new record's ID); ResultID reads it back on a replay too.
func IDResult(id string) idem.Outcome {
	b, _ := json.Marshal(map[string]string{"id": id})
	return idem.Outcome{Status: 200, Result: b}
}

func ResultID(out idem.Outcome) string {
	var r struct{ ID string }
	_ = json.Unmarshal(out.Result, &r)
	return r.ID
}

// Check decides op on a record with the given facts and returns the context to work in and the acting principal.
func Check(c *httpx.Context, op string, facts sdkauthz.Facts) (context.Context, string, error) {
	s, _ := sdkauthz.From(c.Ctx())
	ctx, _, err := s.Check(c.Ctx(), op, facts)
	if err != nil {
		return ctx, "", err
	}
	u, _ := identity.UserFrom(ctx)
	return ctx, u.Subject, nil
}

func Invalid(field, why string) error {
	return errs.New(errs.ReasonRequestInvalid, why).WithViolations(errs.Violation{Field: field, Reason: "INVALID", Description: why})
}

func NotFound(what string) error { return errs.New(errs.ReasonNotFound, what+" not found") }

// Unsupported answers a route of the contract this version does not serve (CAPABILITY_UNSUPPORTED, D75).
func Unsupported(what string) httpx.Handler {
	return func(c *httpx.Context) error {
		return errs.New(errs.ReasonCapabilityUnsupported, what+" is not in this version of erp/payment")
	}
}

// Decimal checks a non-negative decimal string of at most scale places and returns its canonical form.
func Decimal(field, v string, scale int32, positive bool) (string, error) {
	d, err := money.Parse(v)
	if err != nil || d.Sign() < 0 || positive && d.Sign() == 0 || -d.Exponent > scale {
		return "", Invalid(field, field+" is a decimal string with at most "+strconv.Itoa(int(scale))+" places")
	}
	d.Reduce(d)
	return d.Text('f'), nil
}

// Change records the business change with the record's new state (protocol 03), in the transaction that made it.
func Change(m *Base, ctx context.Context, tx db.Tx, actor, op, typ, id string, after any) error {
	_, err := m.RT.Audit.Write(ctx, tx, audit.Record{Category: audit.BusinessChange, Operation: op, Outcome: audit.Succeeded,
		Actor: actor, Target: audit.TargetOf(typ, id, after), Changes: map[string]any{"after": after}})
	return err
}

// ChangeWith is Change with more change entries beside "after" (e.g. the document number a continuous series issued,
// be-protocol P14.5).
func ChangeWith(m *Base, ctx context.Context, tx db.Tx, actor, op, typ, id string, after any, more map[string]any) error {
	changes := map[string]any{"after": after}
	for k, v := range more {
		changes[k] = v
	}
	_, err := m.RT.Audit.Write(ctx, tx, audit.Record{Category: audit.BusinessChange, Operation: op, Outcome: audit.Succeeded,
		Actor: actor, Target: audit.TargetOf(typ, id, after), Changes: changes})
	return err
}

// Emit writes an event into the outbox, in the transaction that changed the record (protocol 02, S2).
func Emit(m *Base, ctx context.Context, tx db.Tx, typ, id string, version int64, legalEntity string, data any) error {
	_, err := m.RT.Events.Emit(ctx, tx, Stream, events.Event{Type: typ, RecordID: id, Version: version,
		Projection: events.Full, LegalEntity: legalEntity, Data: data})
	return err
}

// Minus is a − b, exact (quantities and amounts are decimals with bounded places).
func Minus(a, b string) string {
	x, _, _ := apd.NewFromString(a)
	y, _, _ := apd.NewFromString(b)
	var r apd.Decimal
	_, _ = apd.BaseContext.WithPrecision(80).Sub(&r, x, y)
	r.Reduce(&r)
	return r.Text('f')
}

// Fact publishes an incremental fact (protocol 05, common rules) in the transaction that made it, and keeps it for
// FactService.Read: its record is the fact's ID.
func Fact(m *Base, ctx context.Context, tx db.Tx, typ, factID, legalEntity string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO payment_fact (fact_id, type, data) VALUES ($1, $2, $3)`, factID, typ, b); err != nil {
		return err
	}
	return Emit(m, ctx, tx, typ, factID, 1, legalEntity, json.RawMessage(b))
}

// Sum is the exact sum of decimals.
func Sum(xs ...string) string {
	var r apd.Decimal
	for _, x := range xs {
		d, _, _ := apd.NewFromString(x)
		_, _ = apd.BaseContext.WithPrecision(80).Add(&r, &r, d)
	}
	r.Reduce(&r)
	return r.Text('f')
}

// Cmp compares decimals: -1, 0 or 1.
func Cmp(a, b string) int {
	x, _, _ := apd.NewFromString(a)
	y, _, _ := apd.NewFromString(b)
	return x.Cmp(y)
}

// ClaimRefused says whether err is erp/finance's refusal of a claim command as masterdata answers it (PR04, PR08): the
// claim is not held any more; anything else (finance not reachable) is not.
func ClaimRefused(err error) bool {
	for _, r := range []string{"CLAIM_EXCEEDS_AVAILABLE", "CLAIM_CONFLICT", "CLAIM_STATE"} {
		if errs.Is(err, ID, r) {
			return true
		}
	}
	return false
}
