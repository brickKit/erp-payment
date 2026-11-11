package facts

import (
	"context"
	"time"

	core "github.com/brickKit/erp-payment/v3/internal/core"
	masterdata "github.com/brickKit/erp-payment/v3/internal/masterdata"

	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
)

// Source is the document a fact comes from (the shared contracts' source).
type Source struct {
	Component      string  `json:"component"`
	DocumentType   string  `json:"document_type"`
	DocumentID     string  `json:"document_id"`
	DocumentNumber *string `json:"document_number"`
}

// Dimensions are the analysis dimensions a fact carries (the owners' identities).
type Dimensions struct {
	DepartmentID *string `json:"department_id"`
	ProjectID    *string `json:"project_id"`
}

// ClaimAmount is a payment claim a payment or a refund settles, with its amount (protocol 05).
type ClaimAmount struct {
	ClaimID string `json:"claim_id"`
	Amount  string `json:"amount"`
}

// OrderRef is a receipt's order reference.
type OrderRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Fund is a fund fact in erp/finance's shared contract (fund_fact.v1): money received, paid, refunded, or a reversal.
type Fund struct {
	FactID           string           `json:"fact_id"`
	Source           Source           `json:"source"`
	LegalEntityID    string           `json:"legal_entity_id"`
	BusinessDate     string           `json:"business_date"`
	PostingDate      string           `json:"posting_date"`
	RuleKey          string           `json:"rule_key"`
	Currency         string           `json:"currency"`
	ExchangeRate     *masterdata.Rate `json:"exchange_rate"`
	Dimensions       Dimensions       `json:"dimensions"`
	RecordedAt       string           `json:"recorded_at"`
	Kind             string           `json:"kind"`
	PartnerID        *string          `json:"partner_id"`
	CompanyAccountID string           `json:"company_account_id"`
	Amount           string           `json:"amount"`
	OrderRef         *OrderRef        `json:"order_ref"`
	Claims           []ClaimAmount    `json:"claims"`
	ReversesFactID   *string          `json:"reverses_fact_id"`
}

// Accounting is an accounting fact in erp/finance's shared contract (accounting_fact.v1): a transfer's going out or
// arrival, a bank charge, interest.
type Accounting struct {
	FactID        string            `json:"fact_id"`
	Source        Source            `json:"source"`
	LegalEntityID string            `json:"legal_entity_id"`
	BusinessDate  string            `json:"business_date"`
	PostingDate   string            `json:"posting_date"`
	RuleKey       string            `json:"rule_key"`
	Currency      string            `json:"currency"`
	ExchangeRate  *masterdata.Rate  `json:"exchange_rate"`
	Amounts       map[string]string `json:"amounts"`
	PartnerID     *string           `json:"partner_id"`
	Dimensions    Dimensions        `json:"dimensions"`
	ServicePeriod *string           `json:"service_period"`
	RecordedAt    string            `json:"recorded_at"`
}

// Now is a fact's recorded_at.
func Now() string { return canon.Time(time.Now()) }

// Publish publishes a fund fact in the transaction of its document (core.Fact keeps it for Read).
func (f Fund) Publish(m *core.Base, ctx context.Context, tx db.Tx) error {
	if f.Claims == nil {
		f.Claims = []ClaimAmount{}
	}
	return core.Fact(m, ctx, tx, core.FundFact, f.FactID, f.LegalEntityID, f)
}

// Publish publishes an accounting fact in the transaction of its document.
func (f Accounting) Publish(m *core.Base, ctx context.Context, tx db.Tx) error {
	return core.Fact(m, ctx, tx, core.AccountingFact, f.FactID, f.LegalEntityID, f)
}
