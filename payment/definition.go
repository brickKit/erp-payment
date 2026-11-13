// Package payment is the payment component: the results of money, registered offline (D47): company bank accounts,
// receipts, payment requests that claim erp/finance's open items before approval and confirm them before the money
// goes out (protocol 05), transfers with bank charges and interest, and the manual reconciliation of bank statements;
// the fund and accounting facts by erp/finance's shared contracts.
package payment

import (
	"embed"
	"io/fs"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/component"
	"github.com/brickKit/be-sdk-go/v3/config"
	"github.com/brickKit/be-sdk-go/v3/events"
	"github.com/brickKit/be-sdk-go/v3/flow"
	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Identity.
const (
	Version  = "3.0.31"
	GRPCPort = 9228
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrations() fs.FS {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	return sub
}

// Keys are the component's own configuration keys: the flow kernel's (FLOW_ACTION_SOURCES).
func Keys() []config.Key { return flow.Keys() }

// Own describes the own keys for the manifest's configSchema.
var Own = map[string]string{
	flow.ActionSourcesKey: "the channel members whose button actions the approvals take: component IDs, comma separated " +
		"(e.g. integration/im-dingtalk); unset: none (no buttons, the link still works)",
}

// Definition is the component as the launcher and the shells take it.
func Definition() component.Definition {
	return component.Definition{ID: core.ID, Version: Version, Keys: Keys(), Migrations: migrations(), GRPCPort: GRPCPort, Subscribes: true,
		Streams: []events.Stream{{Name: core.Stream, Partitions: 4}}, Authz: core.Definitions(), Resend: core.OpResend,
		Recovery: core.OpRecover, New: func(rt *runtime.Runtime) (component.Module, error) { return New(rt) }}
}
