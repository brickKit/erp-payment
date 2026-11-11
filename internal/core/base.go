package core

import (
	"github.com/brickKit/be-sdk-go/v3/runtime"
)

// Base is what every module works with: the component's runtime (its database, calls, events, audit, logger).
type Base struct {
	RT *runtime.Runtime
}

// NewBase is the modules' runtime; the component reads no configuration of its own.
func NewBase(rt *runtime.Runtime) (*Base, error) {
	return &Base{RT: rt}, nil
}
