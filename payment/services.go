package payment

import (
	"context"

	facts "github.com/brickKit/erp-payment/v3/internal/facts"

	paymentv3 "github.com/brickKit/erp-payment/contract/v3/gen/go/be/erp/payment/v3"
)

// Read is FactService.Read: this component's incremental facts by identity (internal/facts).
func (m *Module) Read(ctx context.Context, in *paymentv3.ReadRequest) (*paymentv3.ReadResponse, error) {
	return facts.Read(m.b, ctx, in)
}
