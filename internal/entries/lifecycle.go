package entries

import (
	"regexp"

	core "github.com/brickKit/erp-payment/v3/internal/core"
)

// A transfer's, a bank charge's or interest's states in one table (docs/design.md, the transfer and bank entry
// table; BE01-BE04). Approving moves a transfer to in_transit and a charge or interest on to posted (the cell names
// in_transit; GuardNotMaker's command writes posted for the two that do not travel).

const (
	Draft     = "draft"
	Submitted = "submitted"
	InTransit = "in_transit"
	Posted    = "posted"
	Cancelled = "cancelled"
)

const (
	Edit    = "edit"
	Submit  = "submit"
	Approve = "approve"
	Reject  = "reject"
	Arrive  = "arrive"
	Cancel  = "cancel"
)

// The cells' further conditions.
const (
	GuardNotMaker = "not by the maker; a charge or interest goes on to posted"
	GuardReason   = "with a reason"
	GuardArrival  = "an arrival date not before the transfer's"
)

var refuseEdit = core.RefuseCell("BE01", "only a draft is changed")

// Lifecycle is the table.
var Lifecycle = core.Table{
	Rule:     regexp.MustCompile(`^BE\d\d$`),
	Phases:   []string{Draft, Submitted, InTransit, Posted, Cancelled},
	Commands: []string{Edit, Submit, Approve, Reject, Arrive, Cancel},
	Cells: map[string]map[string]core.Decision{
		Edit: {
			Draft:     {Rule: "BE01", Effect: core.Proceed, Op: core.OpEntryEdit},
			Submitted: refuseEdit, InTransit: refuseEdit, Posted: refuseEdit, Cancelled: refuseEdit,
		},
		Submit: {
			Draft:     core.MoveCell("BE02", Submitted, core.OpEntryEdit),
			Submitted: core.IgnoreCell("BE02"),
			InTransit: core.RefuseCell("BE02", "a transfer gone out is not submitted"),
			Posted:    core.RefuseCell("BE02", "a posted entry is not submitted"),
			Cancelled: core.RefuseCell("BE02", "a cancelled entry is not submitted"),
		},
		Approve: {
			Draft:     core.RefuseCell("BE02", "a draft is submitted first"),
			Submitted: {Rule: "BE02", Effect: core.Move, To: InTransit, Op: core.OpEntryApprove, Guard: GuardNotMaker},
			InTransit: core.IgnoreCell("BE02"), Posted: core.IgnoreCell("BE02"),
			Cancelled: core.RefuseCell("BE02", "a cancelled entry is not approved"),
		},
		Reject: {
			Draft:     core.RefuseCell("BE02", "a draft is not rejected"),
			Submitted: {Rule: "BE02", Effect: core.Move, To: Draft, Op: core.OpEntryApprove, Guard: GuardReason},
			InTransit: core.RefuseCell("BE02", "a transfer gone out is not rejected"),
			Posted:    core.RefuseCell("BE02", "a posted entry is not rejected"),
			Cancelled: core.RefuseCell("BE02", "a cancelled entry is not rejected"),
		},
		Arrive: {
			Draft:     core.RefuseCell("BE03", "only a transfer gone out arrives"),
			Submitted: core.RefuseCell("BE03", "only a transfer gone out arrives"),
			InTransit: {Rule: "BE03", Effect: core.Move, To: Posted, Op: core.OpEntryEdit, Guard: GuardArrival},
			Posted:    core.IgnoreCell("BE03"),
			Cancelled: core.RefuseCell("BE03", "a cancelled entry does not arrive"),
		},
		Cancel: {
			Draft: core.MoveCell("BE04", Cancelled, core.OpEntryEdit), Submitted: core.MoveCell("BE04", Cancelled, core.OpEntryEdit),
			InTransit: core.RefuseCell("BE04", "a transfer gone out is not cancelled: make the opposite one"),
			Posted:    core.RefuseCell("BE04", "a posted entry is not cancelled"),
			Cancelled: core.IgnoreCell("BE04"),
		},
	},
}
