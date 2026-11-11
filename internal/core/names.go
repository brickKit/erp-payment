package core

import (
	"time"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
)

// ID is the component's ID.
const ID = "erp/payment"

// Dependencies (the IDs runtime.GRPC takes).
const (
	Partner  = "mdm/partner"
	Org      = "mdm/org"
	Currency = "mdm/currency"
	// Finance owns the open items payment requests claim (protocol 05's payment claims); optional.
	Finance = "erp/finance"
	// Sales answers a receipt's order reference; optional.
	Sales = "erp/sales"
)

// The stream and its event types (contract/events).
const (
	Stream         = "erp.payment"
	FundFact       = "erp.payment.fund_fact.v1"
	AccountingFact = "erp.payment.accounting_fact.v1"
	ReceiptChanged = "erp.receipt.changed.v1"
	RequestChanged = "erp.payment_request.changed.v1"
	// the source stream of the department projection
	OrgStream       = "mdm.org"
	OrgUnitChanged  = "mdm.org_unit.changed.v1"
	OrgUnitEvents   = "mdm.org_unit.>" // every department event, later actions and majors too (protocol P6.33)
	OrgUnitsProject = "org_units"
)

// Resources and operations (contract/openapi.yaml names each route's operation).
const (
	BankAccounts       = "erp.payment.bank_account"
	OpBankAccountRead  = "erp.payment.bank_account.read"
	OpBankAccountWrite = "erp.payment.bank_account.manage"
	Receipts           = "erp.payment.receipt"
	OpReceiptRead      = "erp.payment.receipt.read"
	OpReceiptRegister  = "erp.payment.receipt.register"
	OpReceiptReverse   = "erp.payment.receipt.reverse"
	Requests           = "erp.payment.request"
	OpRequestRead      = "erp.payment.request.read"
	OpRequestEdit      = "erp.payment.request.edit"
	OpRequestApprove   = "erp.payment.request.approve"
	OpRequestExecute   = "erp.payment.request.execute"
	Entries            = "erp.payment.entry"
	OpEntryRead        = "erp.payment.entry.read"
	OpEntryEdit        = "erp.payment.entry.edit"
	OpEntryApprove     = "erp.payment.entry.approve"
	Statements         = "erp.payment.statement"
	OpStatementRead    = "erp.payment.statement.read"
	OpReconcile        = "erp.payment.statement.reconcile"
	System             = "erp.payment.system"
	OpSysRead          = "erp.payment.system.read"
	// OpResend is the redelivery of this component's stream (be-protocol P6.38).
	OpResend = "erp.payment.system.resend"
	// OpRecover is a full recovery's checks (be-protocol chapter 14: the recovery tool only).
	OpRecover = "erp.payment.system.recover"
)

// NumberKey is the read key of a bank account's whole number (a restricted field, security 09a): a grant reads it
// only when it lists the field; everyone else sees its last four digits.
const NumberKey = "erp.payment.bank_account.number"

// Dimensions (security 07): the legal entity and, for payment requests, the department.
const (
	DimLegalEntity = "legal_entity"
	DimDepartment  = "department"
)

// Parameters (protocol 05, protocol 14).
const (
	// RenewAhead is how long before a pending claim's end its renewal runs (PR08).
	RenewAhead = time.Hour
	MaxReadIDs = 500
	MaxLines   = 200
	// MaxStatementLines is the most lines one statement registration takes (BR01).
	MaxStatementLines = 1000
)

// DepartmentTables are the tables that copy their department's path (internal/orgunits rewrites them when a
// department moves).
var DepartmentTables = []string{"payment_request"}

func fields(names ...string) map[string]sdkauthz.Field {
	out := map[string]sdkauthz.Field{}
	for _, n := range names {
		out[n] = sdkauthz.Field{Class: sdkauthz.Ordinary}
	}
	return out
}

// BankAccountFields are what a bank account list may filter and sort on (the number is restricted: below).
var BankAccountFields = []string{"id", "legal_entity_id", "name", "bank", "currency", "state", "version", "created_at",
	"updated_at"}

// ReceiptFields are what a receipt list may filter and sort on.
var ReceiptFields = []string{"id", "number", "legal_entity_id", "bank_account_id", "payer_id", "currency", "amount",
	"received_on", "bank_reference", "order_id", "description", "state", "fact_id", "maker_id", "reason", "version",
	"created_at", "updated_at"}

// RequestFields are what a payment request list may filter and sort on.
var RequestFields = []string{"id", "number", "legal_entity_id", "kind", "payee_id", "bank_account_id", "currency",
	"amount", "purpose", "reference", "department_id", "state", "applicant_id", "approver_id", "releaser_id",
	"paid_amount", "paid_on", "bank_reference", "fact_id", "reason", "lines", "version", "created_at", "updated_at"}

// EntryFields are what a transfer, bank charge or interest list may filter and sort on.
var EntryFields = []string{"id", "number", "legal_entity_id", "kind", "bank_account_id", "to_bank_account_id",
	"currency", "amount", "entry_date", "arrived_on", "description", "state", "maker_id", "approver_id",
	"statement_line_id", "reason", "version", "created_at", "updated_at"}

// StatementFields are what a statement line list may filter and sort on.
var StatementFields = []string{"id", "legal_entity_id", "bank_account_id", "booked_on", "amount", "bank_reference",
	"text", "state", "matched", "note", "version", "created_at", "updated_at"}

func entity() map[string]sdkauthz.Dimension {
	return map[string]sdkauthz.Dimension{DimLegalEntity: {Kind: sdkauthz.ValueSet, Column: "legal_entity_id"}}
}

func entityAndDepartment() map[string]sdkauthz.Dimension {
	return map[string]sdkauthz.Dimension{DimLegalEntity: {Kind: sdkauthz.ValueSet, Column: "legal_entity_id"},
		DimDepartment: {Kind: sdkauthz.OrgTree, Column: "department_path"}}
}

// Definitions are the component's resources and operations.
func Definitions() *sdkauthz.Definitions {
	low := func(key, typ, purpose string, audit bool) sdkauthz.Operation {
		op := sdkauthz.Operation{Key: key, ResourceType: typ, Purpose: purpose, RiskClass: "low", Audit: audit}
		if purpose != "read" {
			op.SideEffects = []string{"business_write"}
		}
		return op
	}
	high := func(key, typ, purpose string) sdkauthz.Operation {
		return sdkauthz.Operation{Key: key, ResourceType: typ, Purpose: purpose, RiskClass: "high", Audit: true,
			SideEffects: []string{"business_write"}}
	}
	accounts := fields(BankAccountFields...)
	accounts["number"] = sdkauthz.Field{Class: sdkauthz.Restricted, ReadKey: NumberKey}
	return &sdkauthz.Definitions{Component: ID,
		Resources: []sdkauthz.Resource{
			{Type: BankAccounts, ViewOperation: OpBankAccountRead, Dimensions: entity(), Fields: accounts},
			{Type: Receipts, ViewOperation: OpReceiptRead, Dimensions: entity(), Fields: fields(ReceiptFields...)},
			{Type: Requests, ViewOperation: OpRequestRead, Dimensions: entityAndDepartment(), Fields: fields(RequestFields...)},
			{Type: Entries, ViewOperation: OpEntryRead, Dimensions: entity(), Fields: fields(EntryFields...)},
			{Type: Statements, ViewOperation: OpStatementRead, Dimensions: entity(), Fields: fields(StatementFields...)},
			{Type: System, ViewOperation: OpSysRead, Dimensions: map[string]sdkauthz.Dimension{},
				Fields: map[string]sdkauthz.Field{}},
		},
		Operations: []sdkauthz.Operation{
			low(OpBankAccountRead, BankAccounts, "read", false),
			high(OpBankAccountWrite, BankAccounts, "manage"),
			low(OpReceiptRead, Receipts, "read", false),
			high(OpReceiptRegister, Receipts, "action"),
			high(OpReceiptReverse, Receipts, "action"),
			low(OpRequestRead, Requests, "read", false),
			low(OpRequestEdit, Requests, "manage", true),
			high(OpRequestApprove, Requests, "action"),
			high(OpRequestExecute, Requests, "action"),
			low(OpEntryRead, Entries, "read", false),
			low(OpEntryEdit, Entries, "manage", true),
			high(OpEntryApprove, Entries, "action"),
			low(OpStatementRead, Statements, "read", false),
			low(OpReconcile, Statements, "manage", true),
			low(OpSysRead, System, "read", false),
			{Key: OpResend, ResourceType: System, Purpose: "read", RiskClass: "low"},
			{Key: OpRecover, ResourceType: System, Purpose: "read", RiskClass: "low"},
		}}
}
