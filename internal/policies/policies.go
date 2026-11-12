// Package policies is the legal entity's payment policy: who approves its payment requests and entries (a position's
// holders or one principal; docs/design.md PR03, BE02). Payments always need people: there is no policy without an
// approval. A legal entity never set has no approver, version 0: submitting answers NO_APPROVER_CONFIGURED.
package policies

import (
	"context"
	"errors"
	"fmt"
	"strings"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/flow"
	"github.com/brickKit/be-sdk-go/v3/httpx"
	"github.com/jackc/pgx/v5"
)

// Policy is a legal entity's policy (the contract's PaymentPolicy).
type Policy struct {
	LegalEntityID     string  `json:"legal_entity_id"`
	ApproverPosition  *string `json:"approver_position_id"`
	ApproverPrincipal *string `json:"approver_principal"`
	Version           int64   `json:"version"`
}

// Approver is the approval's binding (be-protocol P17.3): the position's holders in the legal entity, or the principal;
// NO_APPROVER_CONFIGURED when the policy names neither.
func (p Policy) Approver() (flow.Binding, error) {
	switch {
	case p.ApproverPosition != nil:
		return flow.Binding{LegalEntity: p.LegalEntityID, PositionID: *p.ApproverPosition}, nil
	case p.ApproverPrincipal != nil:
		return flow.Binding{Principal: *p.ApproverPrincipal}, nil
	}
	return flow.Binding{}, core.ErrNoApprover.New("the payment policy of legal entity " + p.LegalEntityID + " names no approver")
}

// Of reads the policy of a legal entity in tx.
func Of(ctx context.Context, tx db.Tx, legalEntity string) (Policy, error) {
	p := Policy{LegalEntityID: legalEntity}
	err := tx.QueryRow(ctx, `SELECT approver_position_id::text, approver_principal, version FROM payment_policy
		WHERE legal_entity_id = $1`, legalEntity).Scan(&p.ApproverPosition, &p.ApproverPrincipal, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	return p, err
}

// ApproverOf answers the legal entity's approver in its own transaction: submitting checks it before claiming at
// erp/finance (PR02: nothing is claimed for a request nobody may approve).
func ApproverOf(m *core.Base, ctx context.Context, legalEntity string) (flow.Binding, error) {
	var b flow.Binding
	err := m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		p, err := Of(ctx, tx, legalEntity)
		if err != nil {
			return err
		}
		b, err = p.Approver()
		return err
	})
	return b, err
}

func GetPolicy(m *core.Base, c *httpx.Context) error {
	le := c.Param("legal_entity_id")
	if !core.UUIDForm.MatchString(le) {
		return core.NotFound("payment policy")
	}
	ctx, _, err := core.Check(c, core.OpPolicyRead, core.EntityFacts(le))
	if err != nil {
		return err
	}
	var p Policy
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		p, err = Of(ctx, tx, le)
		return err
	})
	if err != nil {
		return err
	}
	return c.JSON(200, p)
}

type policyIn struct {
	ApproverPosition  *string `json:"approver_position_id"`
	ApproverPrincipal *string `json:"approver_principal"`
	Version           *int64  `json:"version"`
}

// PutPolicy sets a legal entity's approver; a version given is the one the change is made on (0: never set).
func PutPolicy(m *core.Base, c *httpx.Context) error {
	le := c.Param("legal_entity_id")
	if !core.UUIDForm.MatchString(le) {
		return core.NotFound("payment policy")
	}
	var in policyIn
	if err := c.BindJSON(&in); err != nil {
		return err
	}
	switch {
	case in.ApproverPosition != nil && in.ApproverPrincipal != nil:
		return core.Invalid("approver_principal", "an approver is a position or a principal, not both")
	case in.ApproverPosition != nil && !core.UUIDForm.MatchString(*in.ApproverPosition):
		return core.Invalid("approver_position_id", "a position's ID")
	case in.ApproverPrincipal != nil && strings.TrimSpace(*in.ApproverPrincipal) == "":
		return core.Invalid("approver_principal", "a principal")
	}
	ctx, actor, err := core.Check(c, core.OpPolicyManage, core.EntityFacts(le))
	if err != nil {
		return err
	}
	var out Policy
	err = m.RT.DB.Tx(ctx, func(ctx context.Context, tx db.Tx) error {
		// the version decides in the statement itself: two changes on the same version, one wins
		q := `INSERT INTO payment_policy (legal_entity_id, approver_position_id, approver_principal) VALUES ($1, $2::uuid, $3)
			ON CONFLICT (legal_entity_id) DO UPDATE SET approver_position_id = $2::uuid, approver_principal = $3,
			version = payment_policy.version + 1, updated_at = now()`
		args := []any{le, in.ApproverPosition, in.ApproverPrincipal}
		switch {
		case in.Version != nil && *in.Version == 0:
			q = `INSERT INTO payment_policy (legal_entity_id, approver_position_id, approver_principal)
				VALUES ($1, $2::uuid, $3) ON CONFLICT (legal_entity_id) DO NOTHING`
		case in.Version != nil:
			q = `UPDATE payment_policy SET approver_position_id = $2::uuid, approver_principal = $3, version = version + 1,
				updated_at = now() WHERE legal_entity_id = $1 AND version = $4`
			args = append(args, *in.Version)
		}
		tag, err := tx.Exec(ctx, q, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			cur, err := Of(ctx, tx, le)
			if err != nil {
				return err
			}
			return core.ErrVersion.New(fmt.Sprintf("the policy is at version %d", cur.Version))
		}
		if out, err = Of(ctx, tx, le); err != nil {
			return err
		}
		return core.Change(m, ctx, tx, actor, core.OpPolicyManage, core.Policies, le, out)
	})
	if err != nil {
		return err
	}
	return c.JSON(200, out)
}
