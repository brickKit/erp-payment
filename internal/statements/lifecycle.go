package statements

import (
	"regexp"

	core "github.com/brickKit/erp-payment/v3/internal/core"
)

// A statement line's states in one table (docs/design.md, the statement line table; BR01-BR04).

const (
	Unmatched = "unmatched"
	Matched   = "matched"
	Resolved  = "resolved"
)

const (
	Match       = "match"
	Unmatch     = "unmatch"
	Resolve     = "resolve"
	Reopen      = "reopen"
	CreateEntry = "create_entry"
)

// The cells' further conditions.
const (
	GuardMatchable = "the document can be matched"
	GuardNote      = "with how it was handled"
	GuardNoDraft   = "no entry made from the line yet"
)

// Lifecycle is the table.
var Lifecycle = core.Table{
	Rule:     regexp.MustCompile(`^BR\d\d$`),
	Phases:   []string{Unmatched, Matched, Resolved},
	Commands: []string{Match, Unmatch, Resolve, Reopen, CreateEntry},
	Cells: map[string]map[string]core.Decision{
		Match: {
			Unmatched: {Rule: "BR02", Effect: core.Move, To: Matched, Op: core.OpReconcile, Guard: GuardMatchable},
			Matched:   core.RefuseCell("BR02", "the line is matched: unmatch it first"),
			Resolved:  core.RefuseCell("BR02", "a resolved line is reopened first"),
		},
		Unmatch: {
			Unmatched: core.RefuseCell("BR02", "the line is not matched"),
			Matched:   core.MoveCell("BR02", Unmatched, core.OpReconcile),
			Resolved:  core.RefuseCell("BR03", "a resolved line is reopened, not unmatched"),
		},
		Resolve: {
			Unmatched: {Rule: "BR03", Effect: core.Move, To: Resolved, Op: core.OpReconcile, Guard: GuardNote},
			Matched:   core.RefuseCell("BR03", "a matched line is no difference"),
			Resolved:  core.IgnoreCell("BR03"),
		},
		Reopen: {
			Unmatched: core.RefuseCell("BR03", "the line is open"),
			Matched:   core.RefuseCell("BR03", "a matched line is unmatched, not reopened"),
			Resolved:  core.MoveCell("BR03", Unmatched, core.OpReconcile),
		},
		CreateEntry: {
			Unmatched: {Rule: "BR04", Effect: core.Proceed, Op: core.OpReconcile, Guard: GuardNoDraft},
			Matched:   core.RefuseCell("BR04", "a matched line needs no entry"),
			Resolved:  core.RefuseCell("BR04", "a resolved line needs no entry"),
		},
	},
}
