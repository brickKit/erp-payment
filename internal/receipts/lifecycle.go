package receipts

import (
	"regexp"

	core "github.com/brickKit/erp-payment/v3/internal/core"
)

// A receipt's states in one table (docs/design.md, the receipt table; RC01, RC02).

const (
	None     = "none"
	Received = "received"
	Reversed = "reversed"
)

const (
	Register = "register"
	Reverse  = "reverse"
)

// GuardReason: a reversal says why.
const GuardReason = "with a reason"

// Lifecycle is the table.
var Lifecycle = core.Table{
	Rule:     regexp.MustCompile(`^RC\d\d$`),
	Phases:   []string{None, Received, Reversed},
	Commands: []string{Register, Reverse},
	Cells: map[string]map[string]core.Decision{
		Register: {
			None:     core.MoveCell("RC01", Received, core.OpReceiptRegister),
			Received: core.IgnoreCell("RC01"),
			Reversed: core.IgnoreCell("RC01"),
		},
		Reverse: {
			None:     core.RefuseCell("RC02", "no such receipt"),
			Received: {Rule: "RC02", Effect: core.Move, To: Reversed, Op: core.OpReceiptReverse, Guard: GuardReason},
			Reversed: core.IgnoreCell("RC02"),
		},
	},
}
