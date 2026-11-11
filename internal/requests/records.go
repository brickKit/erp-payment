package requests

import (
	"context"
	"errors"
	"time"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	sdkauthz "github.com/brickKit/be-sdk-go/v3/authz"
	"github.com/brickKit/be-sdk-go/v3/canon"
	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/jackc/pgx/v5"
)

// Request is a payment request as the user surface and erp.payment_request.changed.v1 carry it.
type Request struct {
	ID             string  `json:"id"`
	Number         string  `json:"number"`
	LegalEntityID  string  `json:"legal_entity_id"`
	Kind           string  `json:"kind"`
	PayeeID        string  `json:"payee_id"`
	BankAccountID  string  `json:"bank_account_id"`
	Currency       string  `json:"currency"`
	Amount         string  `json:"amount"`
	Purpose        string  `json:"purpose"`
	Reference      *string `json:"reference"`
	DepartmentID   *string `json:"department_id"`
	State          string  `json:"state"`
	ApplicantID    *string `json:"applicant_id"`
	ApproverID     *string `json:"approver_id"`
	ReleaserID     *string `json:"releaser_id"`
	PaidAmount     *string `json:"paid_amount"`
	PaidOn         *string `json:"paid_on"`
	BankReference  *string `json:"bank_reference"`
	FactID         *string `json:"fact_id"`
	Reason         *string `json:"reason"`
	Lines          []Line  `json:"lines"`
	Version        int64   `json:"version"`
	UpdatedAt      string  `json:"updated_at"`
	departmentPath *string
	reversalFactID *string
	createdAt      string
}

// Line is a request's line: an open item and the amount paid against it, with its claim while it is held.
type Line struct {
	ID             string  `json:"id"`
	OpenItemID     *string `json:"open_item_id"`
	Amount         string  `json:"amount"`
	ClaimID        *string `json:"claim_id"`
	ClaimExpiresAt *string `json:"claim_expires_at"`
	Paid           string  `json:"paid"`
}

const cols = `r.id::text, r.number, r.legal_entity_id::text, r.kind, r.payee_id::text, r.bank_account_id::text, r.currency,
	r.amount::text, r.purpose, r.reference, r.department_id::text, r.department_path, r.state, r.applicant_id, r.approver_id,
	r.releaser_id, r.paid_amount::text, r.paid_on::text, r.bank_reference, r.fact_id::text, r.reversal_fact_id::text,
	r.reason, r.version, r.updated_at, r.created_at`

func scan(row pgx.Row) (Request, error) {
	var r Request
	var at, created time.Time
	err := row.Scan(&r.ID, &r.Number, &r.LegalEntityID, &r.Kind, &r.PayeeID, &r.BankAccountID, &r.Currency, &r.Amount,
		&r.Purpose, &r.Reference, &r.DepartmentID, &r.departmentPath, &r.State, &r.ApplicantID, &r.ApproverID, &r.ReleaserID,
		&r.PaidAmount, &r.PaidOn, &r.BankReference, &r.FactID, &r.reversalFactID, &r.Reason, &r.Version, &at, &created)
	r.UpdatedAt, r.createdAt = canon.Time(at), canon.Time(created)
	r.Lines = []Line{}
	return r, err
}

// lines reads a request's lines in their order.
func lines(ctx context.Context, tx db.Tx, id string) ([]Line, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, open_item_id::text, amount::text, claim_id::text, claim_expires_at, paid::text
		FROM payment_request_line WHERE request_id = $1 ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		var exp *time.Time
		if err := rows.Scan(&l.ID, &l.OpenItemID, &l.Amount, &l.ClaimID, &exp, &l.Paid); err != nil {
			return nil, err
		}
		if exp != nil {
			s := canon.Time(*exp)
			l.ClaimExpiresAt = &s
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get reads a request with its lines (FOR UPDATE when lock).
func Get(ctx context.Context, tx db.Tx, id string, lock bool) (Request, error) {
	q := `SELECT ` + cols + ` FROM payment_request r WHERE r.id = $1::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	r, err := scan(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, core.NotFound("payment request")
	}
	if err != nil {
		return r, err
	}
	r.Lines, err = lines(ctx, tx, id)
	return r, err
}

// Read is Get in its own transaction.
func Read(m *core.Base, ctx context.Context, id string) (Request, error) {
	var r Request
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		var err error
		r, err = Get(ctx, tx, id, false)
		return err
	})
	return r, err
}

// facts are the request's two dimensions (security 07).
func (r Request) facts() sdkauthz.Facts { return core.DocFacts(r.LegalEntityID, r.departmentPath) }

// claims are the request's held claims.
func (r Request) claims() []string {
	var out []string
	for _, l := range r.Lines {
		if l.ClaimID != nil {
			out = append(out, *l.ClaimID)
		}
	}
	return out
}

// changed publishes the request's full state and audits it, in the transaction that changed it.
func changed(m *core.Base, ctx context.Context, tx db.Tx, actor, op, id string) (Request, error) {
	r, err := Get(ctx, tx, id, false)
	if err != nil {
		return r, err
	}
	if err := core.Emit(m, ctx, tx, core.RequestChanged, id, r.Version, r.LegalEntityID, r); err != nil {
		return r, err
	}
	if op == "" { // a system change (PR08) is not an operation of a user
		return r, nil
	}
	return r, core.Change(m, ctx, tx, actor, op, core.Requests, id, r)
}
