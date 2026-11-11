package requests

import (
	"regexp"

	core "github.com/brickKit/erp-payment/v3/internal/core"
)

// A payment request's states in one table (docs/design.md, the payment request table; PR01-PR08).

const (
	Draft     = "draft"
	Submitted = "submitted"
	Approved  = "approved"
	Ready     = "ready"
	Paid      = "paid"
	Returned  = "returned"
	Cancelled = "cancelled"
)

const (
	Edit    = "edit"
	Submit  = "submit"
	Approve = "approve"
	Reject  = "reject"
	Release = "release"
	Execute = "execute"
	Return  = "return"
	Cancel  = "cancel"
	Lapse   = "lapse"
)

// The cells' further conditions (checked by the commands).
const (
	GuardClaimed      = "every line's claim made"
	GuardNotApplicant = "not by the applicant"
	GuardConfirmed    = "not by the applicant, every claim confirmed"
	GuardReason       = "with a reason"
)

var refuseEdit = core.RefuseCell("PR01", "only a draft is changed")

// Lifecycle is the table.
var Lifecycle = core.Table{
	Rule:     regexp.MustCompile(`^PR\d\d$`),
	Phases:   []string{Draft, Submitted, Approved, Ready, Paid, Returned, Cancelled},
	Commands: []string{Edit, Submit, Approve, Reject, Release, Execute, Return, Cancel, Lapse},
	Cells: map[string]map[string]core.Decision{
		Edit: {
			Draft:     {Rule: "PR01", Effect: core.Proceed, Op: core.OpRequestEdit},
			Submitted: refuseEdit, Approved: refuseEdit, Ready: refuseEdit, Paid: refuseEdit, Returned: refuseEdit,
			Cancelled: refuseEdit,
		},
		Submit: {
			Draft:     {Rule: "PR02", Effect: core.Move, To: Submitted, Op: core.OpRequestEdit, Guard: GuardClaimed},
			Submitted: core.IgnoreCell("PR02"), Approved: core.IgnoreCell("PR02"), Ready: core.IgnoreCell("PR02"),
			Paid:      core.RefuseCell("PR02", "a paid request is not submitted"),
			Returned:  core.RefuseCell("PR02", "a returned request is not submitted: make a new one"),
			Cancelled: core.RefuseCell("PR02", "a cancelled request is not submitted"),
		},
		Approve: {
			Draft:     core.RefuseCell("PR03", "a draft is submitted first"),
			Submitted: {Rule: "PR03", Effect: core.Move, To: Approved, Op: core.OpRequestApprove, Guard: GuardNotApplicant},
			Approved:  core.IgnoreCell("PR03"), Ready: core.IgnoreCell("PR03"), Paid: core.IgnoreCell("PR03"),
			Returned:  core.RefuseCell("PR03", "a returned request is not approved"),
			Cancelled: core.RefuseCell("PR03", "a cancelled request is not approved"),
		},
		Reject: {
			Draft:     core.RefuseCell("PR03", "a draft is not rejected"),
			Submitted: {Rule: "PR03", Effect: core.Move, To: Draft, Op: core.OpRequestApprove, Guard: GuardReason},
			Approved:  {Rule: "PR03", Effect: core.Move, To: Draft, Op: core.OpRequestApprove, Guard: GuardReason},
			Ready:     core.RefuseCell("PR03", "a released request is cancelled, not rejected"),
			Paid:      core.RefuseCell("PR03", "a paid request is not rejected"),
			Returned:  core.RefuseCell("PR03", "a returned request is not rejected"),
			Cancelled: core.RefuseCell("PR03", "a cancelled request is not rejected"),
		},
		Release: {
			Draft:     core.RefuseCell("PR04", "a draft is not released"),
			Submitted: core.RefuseCell("PR04", "a request is approved first"),
			Approved:  {Rule: "PR04", Effect: core.Move, To: Ready, Op: core.OpRequestExecute, Guard: GuardConfirmed},
			Ready:     core.IgnoreCell("PR04"), Paid: core.IgnoreCell("PR04"),
			Returned:  core.RefuseCell("PR04", "a returned request is not released"),
			Cancelled: core.RefuseCell("PR04", "a cancelled request is not released"),
		},
		Execute: {
			Draft:     core.RefuseCell("PR05", "a draft is not executed"),
			Submitted: core.RefuseCell("PR05", "a request is approved and released first"),
			Approved:  core.RefuseCell("PR05", "a request is released first"),
			Ready:     core.MoveCell("PR05", Paid, core.OpRequestExecute),
			Paid:      core.IgnoreCell("PR05"),
			Returned:  core.RefuseCell("PR05", "a returned request is not executed"),
			Cancelled: core.RefuseCell("PR05", "a cancelled request is not executed"),
		},
		Return: {
			Draft: core.RefuseCell("PR06", "only a paid request is returned"), Submitted: core.RefuseCell("PR06", "only a paid request is returned"),
			Approved: core.RefuseCell("PR06", "only a paid request is returned"), Ready: core.RefuseCell("PR06", "only a paid request is returned"),
			Paid:      {Rule: "PR06", Effect: core.Move, To: Returned, Op: core.OpRequestExecute, Guard: GuardReason},
			Returned:  core.IgnoreCell("PR06"),
			Cancelled: core.RefuseCell("PR06", "a cancelled request is not returned"),
		},
		Cancel: {
			Draft: core.MoveCell("PR07", Cancelled, core.OpRequestEdit), Submitted: core.MoveCell("PR07", Cancelled, core.OpRequestEdit),
			Approved: core.MoveCell("PR07", Cancelled, core.OpRequestEdit), Ready: core.MoveCell("PR07", Cancelled, core.OpRequestEdit),
			Paid:      core.RefuseCell("PR07", "a paid request is returned, not cancelled"),
			Returned:  core.RefuseCell("PR07", "a returned request is not cancelled"),
			Cancelled: core.IgnoreCell("PR07"),
		},
		Lapse: {
			Draft: core.IgnoreCell("PR08"), Submitted: core.MoveCell("PR08", Draft, ""), Approved: core.MoveCell("PR08", Draft, ""),
			Ready: core.IgnoreCell("PR08"), Paid: core.IgnoreCell("PR08"), Returned: core.IgnoreCell("PR08"),
			Cancelled: core.IgnoreCell("PR08"),
		},
	},
}
