package masterdata

import (
	"context"
	"encoding/json"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/calls"
	"github.com/brickKit/be-sdk-go/v3/errs"
	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
	salesv3 "github.com/brickKit/erp-sales/contract/v3/gen/go/be/erp/sales/v3"
	currencyv3 "github.com/brickKit/mdm-currency/contract/v3/gen/go/be/mdm/currency/v3"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Every call here runs outside a transaction (protocol P4.20): evidence first, then the transaction. Unreachable is
// never "does not exist" (protocol 07): it answers DEPENDENCY_UNAVAILABLE.

func Conn(m *core.Base, id string) (*grpc.ClientConn, error) {
	c, ok := m.RT.GRPC(id)
	if !ok {
		return nil, errs.New(errs.ReasonDependencyUnavailable, id+" is not reachable")
	}
	return c, nil
}

// LegalEntity is what payment needs of a legal entity.
type LegalEntity struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	State        string `json:"state"`
	Timezone     string `json:"timezone"`
	BaseCurrency string `json:"base_currency"`
}

func LegalEntityOf(m *core.Base, ctx context.Context, id string) (LegalEntity, error) {
	c, err := Conn(m, core.Org)
	if err != nil {
		return LegalEntity{}, err
	}
	res, err := orgv3.NewSourceServiceClient(c).Read(ctx, &orgv3.ReadRequest{
		Aggregate: orgv3.Aggregate_AGGREGATE_LEGAL_ENTITY, Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return LegalEntity{}, err
	}
	var le LegalEntity
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != orgv3.ReadState_READ_STATE_EXISTS {
		return le, core.ErrLegalEntity.New("no legal entity " + id)
	}
	if err := json.Unmarshal(res.GetResults()[0].GetData(), &le); err != nil {
		return le, err
	}
	if le.State != "active" {
		return le, core.ErrLegalEntity.New("the legal entity is " + le.State)
	}
	return le, nil
}

// OrgUnit is a department as the projection keeps it (mdm.org_unit.changed.v1).
type OrgUnit struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legal_entity_id"`
	Path          string `json:"path"`
	State         string `json:"state"`
}

// OrgUnitRead reads one department from mdm/org's source method, with its version and the source's identity.
func OrgUnitRead(m *core.Base, ctx context.Context, id string) (OrgUnit, int64, string, string, bool, error) {
	c, err := Conn(m, core.Org)
	if err != nil {
		return OrgUnit{}, 0, "", "", false, err
	}
	res, err := orgv3.NewSourceServiceClient(c).Read(ctx, &orgv3.ReadRequest{
		Aggregate: orgv3.Aggregate_AGGREGATE_ORG_UNIT, Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return OrgUnit{}, 0, "", "", false, err
	}
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != orgv3.ReadState_READ_STATE_EXISTS {
		return OrgUnit{}, 0, res.GetStamp().GetInstance(), res.GetStamp().GetEpoch(), false, nil
	}
	var u OrgUnit
	r := res.GetResults()[0]
	return u, r.GetVersion(), res.GetStamp().GetInstance(), res.GetStamp().GetEpoch(), true, json.Unmarshal(r.GetData(), &u)
}

// Partner is a payer's or a payee's snapshot.
type Partner struct {
	ID    string
	Name  string
	State string
}

// PartnerOf reads a partner from mdm/partner's source method; one that does not exist or is not active is refused.
func PartnerOf(m *core.Base, ctx context.Context, id string) (Partner, error) {
	c, err := Conn(m, core.Partner)
	if err != nil {
		return Partner{}, err
	}
	res, err := partnerv3.NewSourceServiceClient(c).Read(ctx, &partnerv3.ReadRequest{
		Aggregate: partnerv3.Aggregate_AGGREGATE_PARTNER, Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return Partner{}, err
	}
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != partnerv3.ReadState_READ_STATE_EXISTS {
		return Partner{}, core.ErrPartner.New("no partner " + id)
	}
	var p struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(res.GetResults()[0].GetData(), &p); err != nil {
		return Partner{}, err
	}
	if p.State != "active" {
		return Partner{}, core.ErrPartner.New("partner " + id + " is " + p.State)
	}
	return Partner{ID: id, Name: p.Name, State: p.State}, nil
}

// CurrencyScale is an active currency's minor unit.
func CurrencyScale(m *core.Base, ctx context.Context, code string) (int32, error) {
	c, err := Conn(m, core.Currency)
	if err != nil {
		return 0, err
	}
	res, err := currencyv3.NewSourceServiceClient(c).Read(ctx, &currencyv3.ReadRequest{
		Aggregate: currencyv3.Aggregate_AGGREGATE_CURRENCY, Ids: []string{code}}, calls.Idempotent())
	if err != nil {
		return 0, err
	}
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != currencyv3.ReadState_READ_STATE_EXISTS {
		return 0, core.ErrCurrency.New("no currency " + code)
	}
	var cur struct {
		MinorUnit int32 `json:"minor_unit"`
		Active    bool  `json:"active"`
	}
	if err := json.Unmarshal(res.GetResults()[0].GetData(), &cur); err != nil {
		return 0, err
	}
	if !cur.Active {
		return 0, core.ErrCurrency.New("currency " + code + " is not active")
	}
	return cur.MinorUnit, nil
}

// Deployed says whether an optional dependency is in the assembly (erp/finance, erp/sales).
func Deployed(m *core.Base, id string) bool {
	_, ok := m.RT.GRPC(id)
	return ok
}

// Rate is a rate snapshot as a fact carries it (the shared contracts' exchange_rate).
type Rate struct {
	Rate string `json:"rate"`
	Date string `json:"date"`
	Type string `json:"type"`
}

// RateOf is the spot rate from a currency to the legal entity's base currency on the business date (mdm/currency's
// RateService); nil when they are the same currency. No rate answers CURRENCY_NOT_USABLE.
func RateOf(m *core.Base, ctx context.Context, from, to, date string) (*Rate, error) {
	if from == to {
		return nil, nil
	}
	c, err := Conn(m, core.Currency)
	if err != nil {
		return nil, err
	}
	res, err := currencyv3.NewRateServiceClient(c).EffectiveRate(ctx, &currencyv3.EffectiveRateRequest{RateType: "spot",
		Date: date, FromCurrency: from, ToCurrency: to}, calls.Idempotent())
	if status.Code(err) == codes.NotFound {
		return nil, core.ErrCurrency.New("no spot rate " + from + "/" + to + " on " + date)
	}
	if err != nil {
		return nil, err
	}
	return &Rate{Rate: res.GetRate(), Date: res.GetRateDate(), Type: res.GetRateType()}, nil
}

// Order is what a receipt checks of an erp/sales order (RC01).
type Order struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legal_entity_id"`
	CustomerID    string `json:"customer_id"`
}

// OrderOf reads an order from erp/sales' OrderService; not known, or erp/sales not deployed, answers ORDER_NOT_USABLE.
func OrderOf(m *core.Base, ctx context.Context, id string) (Order, error) {
	if !Deployed(m, core.Sales) {
		return Order{}, core.ErrOrder.New("erp/sales is not in the assembly: a receipt takes no order reference")
	}
	c, err := Conn(m, core.Sales)
	if err != nil {
		return Order{}, err
	}
	res, err := salesv3.NewOrderServiceClient(c).Read(ctx, &salesv3.ReadRequest{Ids: []string{id}}, calls.Idempotent())
	if err != nil {
		return Order{}, err
	}
	if len(res.GetResults()) != 1 || res.GetResults()[0].GetState() != salesv3.ReadState_READ_STATE_EXISTS {
		return Order{}, core.ErrOrder.New("no sales order " + id)
	}
	var o Order
	return o, json.Unmarshal(res.GetResults()[0].GetData(), &o)
}

// finance is erp/finance's ClaimService; refusals come back as this component's reasons of the same name (PR02).
func finance(m *core.Base) (financev3.ClaimServiceClient, error) {
	c, err := Conn(m, core.Finance)
	if err != nil {
		return nil, err
	}
	return financev3.NewClaimServiceClient(c), nil
}

// claimRefusal turns erp/finance's refusal of a claim command into this component's reason; other errors pass.
func claimRefusal(err error) error {
	e, code, ok := errs.FromGRPC(err)
	if !ok || code != errs.FailedPrecondition {
		return err
	}
	switch e.Reason {
	case "CLAIM_EXCEEDS_AVAILABLE":
		return core.ErrClaimExceeds.New(e.Detail)
	case "CLAIM_CONFLICT":
		return core.ErrClaimConflict.New(e.Detail)
	default:
		return core.ErrClaimState.New(e.Detail)
	}
}

// DueItems is one page of erp/finance's due open items (a payment proposal, PR01; the payee's check, PR02).
func DueItems(m *core.Base, ctx context.Context, in *financev3.DueItemsRequest) (*financev3.DueItemsResponse, error) {
	f, err := finance(m)
	if err != nil {
		return nil, err
	}
	return f.DueItems(ctx, in, calls.Idempotent())
}

// Claim claims an open item's amount under a claim ID (idempotent at erp/finance): the claim's end.
func Claim(m *core.Base, ctx context.Context, claimID, item, amount string) (string, error) {
	f, err := finance(m)
	if err != nil {
		return "", err
	}
	r, err := f.Claim(ctx, &financev3.ClaimRequest{ClaimId: claimID, OpenItemId: item, Amount: amount}, calls.Idempotent())
	if err != nil {
		return "", claimRefusal(err)
	}
	return r.GetClaim().GetExpiresAt(), nil
}

// Renew extends a pending claim: its new end.
func Renew(m *core.Base, ctx context.Context, claimID string) (string, error) {
	f, err := finance(m)
	if err != nil {
		return "", err
	}
	r, err := f.Renew(ctx, &financev3.RenewRequest{ClaimId: claimID}, calls.Idempotent())
	if err != nil {
		return "", claimRefusal(err)
	}
	return r.GetClaim().GetExpiresAt(), nil
}

// Confirm confirms a claim before the money goes out (PR04).
func Confirm(m *core.Base, ctx context.Context, claimID string) error {
	f, err := finance(m)
	if err != nil {
		return err
	}
	_, err = f.Confirm(ctx, &financev3.ConfirmRequest{ClaimId: claimID}, calls.Idempotent())
	return claimRefusal(err)
}

// Release gives a claim back (PR03, PR07, PR08); one finance does not know is released already.
func Release(m *core.Base, ctx context.Context, claimID string) error {
	f, err := finance(m)
	if err != nil {
		return err
	}
	_, err = f.Release(ctx, &financev3.ReleaseRequest{ClaimId: claimID}, calls.Idempotent())
	if err != nil {
		if e, _, ok := errs.FromGRPC(err); ok && e.Reason == "CLAIM_STATE" {
			return nil
		}
	}
	return err
}

// ClaimState is a claim's state at erp/finance (Get); "" when finance does not know it.
func ClaimState(m *core.Base, ctx context.Context, claimID string) (string, error) {
	f, err := finance(m)
	if err != nil {
		return "", err
	}
	r, err := f.Get(ctx, &financev3.GetRequest{ClaimId: claimID}, calls.Idempotent())
	if status.Code(err) == codes.NotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return r.GetClaim().GetState(), nil
}
