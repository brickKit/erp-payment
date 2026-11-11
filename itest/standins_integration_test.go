//go:build integration

package itest

// The stand-ins of the integration test: the master data components, erp/finance's ClaimService and erp/sales' order
// read, each speaking its published contract over mTLS.

import (
	"context"
	"encoding/json"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/brickKit/be-sdk-go/v3/errs"
	financev3 "github.com/brickKit/erp-finance/contract/v3/gen/go/be/erp/finance/v3"
	salesv3 "github.com/brickKit/erp-sales/contract/v3/gen/go/be/erp/sales/v3"
	currencyv3 "github.com/brickKit/mdm-currency/contract/v3/gen/go/be/mdm/currency/v3"
	orgv3 "github.com/brickKit/mdm-org/contract/v3/gen/go/be/mdm/org/v3"
	partnerv3 "github.com/brickKit/mdm-partner/contract/v3/gen/go/be/mdm/partner/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// --- mdm/org: legal entities and departments ---

type orgStandIn struct {
	orgv3.UnimplementedSourceServiceServer
	mu    sync.Mutex
	les   map[string]map[string]any
	units map[string]map[string]any
}

var orgStamp = &orgv3.Stamp{Instance: "org-it", Epoch: "1.1"}

func (o *orgStandIn) Read(_ context.Context, in *orgv3.ReadRequest) (*orgv3.ReadResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	src := o.les
	if in.GetAggregate() == orgv3.Aggregate_AGGREGATE_ORG_UNIT {
		src = o.units
	}
	out := &orgv3.ReadResponse{Stamp: orgStamp}
	for _, id := range in.GetIds() {
		r := &orgv3.ReadResult{Id: id, State: orgv3.ReadState_READ_STATE_PURGED}
		if v, ok := src[id]; ok {
			data, _ := json.Marshal(v)
			r = &orgv3.ReadResult{Id: id, State: orgv3.ReadState_READ_STATE_EXISTS, Version: v["version"].(int64), Data: data}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

func (o *orgStandIn) Snapshot(_ context.Context, in *orgv3.SnapshotRequest) (*orgv3.SnapshotResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := &orgv3.SnapshotResponse{Stamp: orgStamp}
	if in.GetAggregate() != orgv3.Aggregate_AGGREGATE_ORG_UNIT || in.GetCursor() != "" {
		return out, nil
	}
	for id, v := range o.units {
		data, _ := json.Marshal(v)
		out.Records = append(out.Records, &orgv3.Record{Id: id, Version: v["version"].(int64), Data: data})
	}
	return out, nil
}

// --- mdm/partner ---

type partnerStandIn struct {
	partnerv3.UnimplementedSourceServiceServer
	partnerv3.UnimplementedPartnerServiceServer
	names   map[string]string
	blocked map[string]bool
}

func (p *partnerStandIn) CheckEligibility(_ context.Context, in *partnerv3.CheckEligibilityRequest) (*partnerv3.CheckEligibilityResponse, error) {
	if _, ok := p.names[in.GetPartnerId()]; !ok {
		return &partnerv3.CheckEligibilityResponse{Reason: "not_found"}, nil
	}
	if p.blocked[in.GetPartnerId()] {
		return &partnerv3.CheckEligibilityResponse{Reason: "partner_suspended"}, nil
	}
	return &partnerv3.CheckEligibilityResponse{Eligible: true, Reason: "eligible"}, nil
}

func (p *partnerStandIn) Read(_ context.Context, in *partnerv3.ReadRequest) (*partnerv3.ReadResponse, error) {
	out := &partnerv3.ReadResponse{Stamp: &partnerv3.Stamp{}}
	for _, id := range in.GetIds() {
		r := &partnerv3.ReadResult{Id: id, State: partnerv3.ReadState_READ_STATE_PURGED}
		if n, ok := p.names[id]; ok {
			state := "active"
			if p.blocked[id] {
				state = "suspended"
			}
			data, _ := json.Marshal(map[string]any{"id": id, "name": n, "tax_id": "91310000-" + id[:4], "state": state})
			r = &partnerv3.ReadResult{Id: id, State: partnerv3.ReadState_READ_STATE_EXISTS, Version: 1, Data: data}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

// --- mdm/currency: CNY and USD ---

type currencyStandIn struct {
	currencyv3.UnimplementedSourceServiceServer
	currencyv3.UnimplementedRateServiceServer
}

func (currencyStandIn) Read(_ context.Context, in *currencyv3.ReadRequest) (*currencyv3.ReadResponse, error) {
	out := &currencyv3.ReadResponse{Stamp: &currencyv3.Stamp{}}
	for _, code := range in.GetIds() {
		r := &currencyv3.ReadResult{Id: code, State: currencyv3.ReadState_READ_STATE_PURGED}
		if code == "CNY" || code == "USD" {
			data, _ := json.Marshal(map[string]any{"code": code, "minor_unit": 2, "active": true})
			r = &currencyv3.ReadResult{Id: code, State: currencyv3.ReadState_READ_STATE_EXISTS, Version: 1, Data: data}
		}
		out.Results = append(out.Results, r)
	}
	return out, nil
}

// financeStandIn is erp/finance's ClaimService: the payment use of the claim pattern on the open items the test sets
// (protocol 05; erp/finance's CL01-CL08 as far as a payer sees them). A claim lasts period; renew and confirm can be
// refused for a claim the test marks lost.
type financeStandIn struct {
	financev3.UnimplementedClaimServiceServer
	mu     sync.Mutex
	items  map[string]*financev3.OpenItem
	claims map[string]*standInClaim
	period time.Duration
	lost   map[string]bool
	renews int
}

type standInClaim struct {
	item, amount, state string
	expires             time.Time
}

func newFinanceStandIn() *financeStandIn {
	return &financeStandIn{items: map[string]*financev3.OpenItem{}, claims: map[string]*standInClaim{}, period: time.Hour,
		lost: map[string]bool{}}
}

// The reasons erp/finance answers (its domain), as the system surface carries them.
var (
	finExceeds  = errs.Define("erp/finance", "CLAIM_EXCEEDS_AVAILABLE", errs.FailedPrecondition, 409, errs.RetryNo, "More than available")
	finConflict = errs.Define("erp/finance", "CLAIM_CONFLICT", errs.FailedPrecondition, 409, errs.RetryNo, "Another amount")
	finState    = errs.Define("erp/finance", "CLAIM_STATE", errs.FailedPrecondition, 409, errs.RetryNo, "Not in that state")
)

func refuse(e errs.Declared, why string) error { return e.New(why).GRPCStatus().Err() }

// item adds an open item with its available balance.
func (f *financeStandIn) item(id, kind, le, partner, currency, available, due string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[id] = &financev3.OpenItem{Id: id, Kind: kind, LegalEntityId: le, PartnerId: partner, Currency: currency,
		Amount: available, Settled: "0", PayClaimed: "0", Available: available, DueDate: due, SourceKind: "supplier_invoice",
		SourceNumber: "SI-" + id[:4]}
}

func (f *financeStandIn) moveAvailable(item, by string, sign int) {
	o := f.items[item]
	a, _ := new(big.Rat).SetString(o.Available)
	b, _ := new(big.Rat).SetString(by)
	if sign < 0 {
		b.Neg(b)
	}
	o.Available = a.Add(a, b).FloatString(2)
}

func (f *financeStandIn) proto(id string) *financev3.Claim {
	c := f.claims[id]
	out := &financev3.Claim{ClaimId: id, OpenItemId: c.item, Amount: c.amount, State: c.state, FormalizedAmount: "0", ReversedAmount: "0"}
	if c.state == "pending" {
		out.ExpiresAt = c.expires.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func (f *financeStandIn) Claim(_ context.Context, in *financev3.ClaimRequest) (*financev3.ClaimResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.claims[in.GetClaimId()]; ok {
		if c.amount != in.GetAmount() || c.item != in.GetOpenItemId() {
			return nil, refuse(finConflict, "claim "+in.GetClaimId())
		}
		return &financev3.ClaimResponse{Claim: f.proto(in.GetClaimId())}, nil
	}
	o, ok := f.items[in.GetOpenItemId()]
	if !ok || o.Kind == "receivable" {
		return nil, refuse(finState, "open item "+in.GetOpenItemId()+" is not claimed for a payment")
	}
	a, _ := new(big.Rat).SetString(o.Available)
	b, _ := new(big.Rat).SetString(in.GetAmount())
	if b.Cmp(a) > 0 {
		return nil, refuse(finExceeds, "available "+o.Available)
	}
	f.claims[in.GetClaimId()] = &standInClaim{item: o.Id, amount: in.GetAmount(), state: "pending", expires: time.Now().Add(f.period)}
	f.moveAvailable(o.Id, in.GetAmount(), -1)
	return &financev3.ClaimResponse{Claim: f.proto(in.GetClaimId())}, nil
}

func (f *financeStandIn) Renew(_ context.Context, in *financev3.RenewRequest) (*financev3.RenewResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.claims[in.GetClaimId()]
	if !ok || c.state != "pending" || f.lost[in.GetClaimId()] {
		return nil, refuse(finState, "claim "+in.GetClaimId()+" is not pending")
	}
	f.renews++
	c.expires = time.Now().Add(f.period)
	return &financev3.RenewResponse{Claim: f.proto(in.GetClaimId())}, nil
}

func (f *financeStandIn) Confirm(_ context.Context, in *financev3.ConfirmRequest) (*financev3.ConfirmResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.claims[in.GetClaimId()]
	if !ok || f.lost[in.GetClaimId()] || (c.state != "pending" && c.state != "confirmed") {
		return nil, refuse(finState, "claim "+in.GetClaimId()+" is not held")
	}
	c.state = "confirmed"
	return &financev3.ConfirmResponse{Claim: f.proto(in.GetClaimId())}, nil
}

func (f *financeStandIn) Release(_ context.Context, in *financev3.ReleaseRequest) (*financev3.ReleaseResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.claims[in.GetClaimId()]
	if !ok {
		return nil, refuse(finState, "no claim "+in.GetClaimId())
	}
	if c.state == "pending" || c.state == "confirmed" {
		c.state = "released"
		f.moveAvailable(c.item, c.amount, 1)
	}
	return &financev3.ReleaseResponse{Claim: f.proto(in.GetClaimId())}, nil
}

func (f *financeStandIn) Get(_ context.Context, in *financev3.GetRequest) (*financev3.GetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.claims[in.GetClaimId()]; !ok {
		return nil, status.Error(codes.NotFound, "no claim")
	}
	return &financev3.GetResponse{Claim: f.proto(in.GetClaimId())}, nil
}

func (f *financeStandIn) DueItems(_ context.Context, in *financev3.DueItemsRequest) (*financev3.DueItemsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &financev3.DueItemsResponse{}
	for _, o := range f.items {
		if o.LegalEntityId == in.GetLegalEntityId() && o.Kind != "receivable" && o.Available != "0.00" && o.Available != "0" &&
			(in.GetKind() == "" || in.GetKind() == o.Kind) && (in.GetPartnerId() == "" || in.GetPartnerId() == o.PartnerId) &&
			o.DueDate <= in.GetDueOnOrBefore() {
			out.Items = append(out.Items, proto.Clone(o).(*financev3.OpenItem))
		}
	}
	sort.Slice(out.Items, func(i, j int) bool {
		return out.Items[i].DueDate+out.Items[i].Id < out.Items[j].DueDate+out.Items[j].Id
	})
	return out, nil
}

// claimFor makes another payer's pending claim on an item (its available balance goes down).
func (f *financeStandIn) claimFor(item, amount string) {
	_, err := f.Claim(context.Background(), &financev3.ClaimRequest{ClaimId: "other-" + item, OpenItemId: item, Amount: amount})
	if err != nil {
		panic(err)
	}
}

// lose marks a claim lapsed: its renewal and its confirmation are refused.
func (f *financeStandIn) lose(claim string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lost[claim] = true
}

func (f *financeStandIn) renewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renews
}

// state is a claim's state at the stand-in.
func (f *financeStandIn) state(claim string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.claims[claim]; ok {
		return c.state
	}
	return ""
}

func (f *financeStandIn) available(item string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.items[item].Available
}

// salesStandIn is erp/sales' OrderService.Read on the orders the test sets (a receipt's order reference, RC01).
type salesStandIn struct {
	salesv3.UnimplementedOrderServiceServer
	mu     sync.Mutex
	orders map[string]map[string]any
}

// EffectiveRate answers USD to CNY at 7.1 on the date asked (a foreign account's receipts and payments, RC01).
func (currencyStandIn) EffectiveRate(_ context.Context, in *currencyv3.EffectiveRateRequest) (*currencyv3.EffectiveRateResponse, error) {
	if in.GetFromCurrency() != "USD" || in.GetToCurrency() != "CNY" {
		return nil, status.Error(codes.NotFound, "no rate "+in.GetFromCurrency()+"/"+in.GetToCurrency())
	}
	return &currencyv3.EffectiveRateResponse{Id: "0190f3a1-0000-7000-8000-00000000f001", Version: 1, Rate: "7.1",
		RateDate: in.GetDate(), RateType: in.GetRateType(), FromCurrency: "USD", ToCurrency: "CNY"}, nil
}

// Read answers the orders the test set, as erp.sales_order.changed.v1 carries them (the fields a receipt checks).
func (s *salesStandIn) Read(_ context.Context, in *salesv3.ReadRequest) (*salesv3.ReadResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &salesv3.ReadResponse{}
	for _, id := range in.GetIds() {
		o, ok := s.orders[id]
		if !ok {
			out.Results = append(out.Results, &salesv3.ReadResult{Id: id, State: salesv3.ReadState_READ_STATE_NOT_FOUND})
			continue
		}
		data, _ := json.Marshal(o)
		out.Results = append(out.Results, &salesv3.ReadResult{Id: id, State: salesv3.ReadState_READ_STATE_EXISTS, Version: 1, Data: data})
	}
	return out, nil
}

func (s *salesStandIn) set(id, legalEntity, customer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orders[id] = map[string]any{"id": id, "number": "SO-" + id[:6], "legal_entity_id": legalEntity, "customer_id": customer,
		"state": "confirmed", "currency": "CNY"}
}
