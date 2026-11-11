// Package matching matches bank statement lines and this component's documents (BR01-BR04): the one writer of a
// line's match. It reads the documents' tables (receipts, payment requests, bank entries) by their states and signs,
// so the document modules can call it in their own transactions and the statements module above them too.
package matching

import (
	"context"
	"errors"

	core "github.com/brickKit/erp-payment/v3/internal/core"

	"github.com/brickKit/be-sdk-go/v3/db"
	"github.com/brickKit/be-sdk-go/v3/errs"
	"github.com/jackc/pgx/v5"
)

// Doc is a document as a statement line of its account sees it: money in positive, out negative; a transfer by its
// leg (out on the from-account, in on the to-account).
type Doc struct {
	Type, ID, Number, Account, Amount, Leg string
	Date                                   string
	Ref                                    *string
}

// documents is every document that can match a line (BR02): received receipts, paid requests, posted charges and
// interest, a transfer's legs once gone out and once arrived. Reversed and returned ones do not.
const documents = `
	SELECT 'receipt', r.id::text, r.number, r.bank_account_id::text, r.amount, '', r.received_on, r.bank_reference
		FROM receipt r WHERE r.state = 'received'
	UNION ALL
	SELECT 'payment_request', p.id::text, p.number, p.bank_account_id::text, -p.paid_amount, '', p.paid_on, p.bank_reference
		FROM payment_request p WHERE p.state = 'paid'
	UNION ALL
	SELECT 'bank_entry', e.id::text, e.number, e.bank_account_id::text,
		CASE e.kind WHEN 'interest' THEN e.amount ELSE -e.amount END, CASE e.kind WHEN 'transfer' THEN 'out' ELSE '' END,
		e.entry_date, NULL FROM bank_entry e WHERE e.state = 'posted' OR (e.kind = 'transfer' AND e.state = 'in_transit')
	UNION ALL
	SELECT 'bank_entry', e.id::text, e.number, e.to_bank_account_id::text, e.amount, 'in', e.arrived_on, NULL
		FROM bank_entry e WHERE e.kind = 'transfer' AND e.state = 'posted'`

// Unmatched is this side's documents on an account in a date range no line matched (the reconciliation).
func Unmatched(ctx context.Context, tx db.Tx, account, from, to string) ([]Doc, error) {
	rows, err := tx.Query(ctx, `SELECT d.type, d.id, d.number, d.account, d.amount::text, d.leg, d.date::text, d.ref
		FROM (`+documents+`) AS d (type, id, number, account, amount, leg, date, ref)
		WHERE d.account = $1 AND d.date BETWEEN $2::date AND $3::date AND NOT EXISTS (SELECT 1 FROM statement_line l
			WHERE l.state = 'matched' AND l.matched_type = d.type AND l.matched_id = d.id::uuid AND l.matched_leg = d.leg)
		ORDER BY d.date, d.number`, account, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Doc
	for rows.Next() {
		var d Doc
		if err := rows.Scan(&d.Type, &d.ID, &d.Number, &d.Account, &d.Amount, &d.Leg, &d.Date, &d.Ref); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Match matches a line to a document of its account (BR02): the line unmatched, the amounts equal (the same
// direction), the document (its leg on this account) matched by no other line; NOT_MATCHABLE otherwise.
func Match(ctx context.Context, tx db.Tx, line, docType, docID string) error {
	var account, amount, state string
	err := tx.QueryRow(ctx, `SELECT bank_account_id::text, amount::text, state FROM statement_line WHERE id = $1 FOR UPDATE`,
		line).Scan(&account, &amount, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.NotFound("statement line")
	}
	if err != nil {
		return err
	}
	if state != "unmatched" {
		return core.ErrNotMatchable.New("the line is " + state)
	}
	var number, leg string
	err = tx.QueryRow(ctx, `SELECT d.number, d.leg FROM (`+documents+`) AS d (type, id, number, account, amount, leg, date, ref)
		WHERE d.type = $1 AND d.id = $2 AND d.account = $3 AND d.amount = $4::numeric AND NOT EXISTS (SELECT 1
			FROM statement_line l WHERE l.state = 'matched' AND l.matched_type = d.type AND l.matched_id = d.id::uuid
			AND l.matched_leg = d.leg)`, docType, docID, account, amount).Scan(&number, &leg)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.ErrNotMatchable.New("no " + docType + " " + docID + " of this account and amount left to match")
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE statement_line SET state = 'matched', matched_type = $2, matched_id = $3, matched_number = $4,
		matched_leg = $5, version = version + 1, updated_at = now() WHERE id = $1`, line, docType, docID, number, leg)
	return err
}

// ByReference matches a line to the one document of its account with its bank reference and amount (BR01), when
// there is exactly one; it says whether it matched.
func ByReference(ctx context.Context, tx db.Tx, line string) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT d.type, d.id FROM statement_line l, (`+documents+`) AS d (type, id, number, account,
		amount, leg, date, ref) WHERE l.id = $1 AND l.state = 'unmatched' AND l.bank_reference IS NOT NULL
		AND d.account = l.bank_account_id::text AND d.ref = l.bank_reference AND d.amount = l.amount`, line)
	if err != nil {
		return false, err
	}
	var found [][2]string
	for rows.Next() {
		var t, id string
		if err := rows.Scan(&t, &id); err != nil {
			rows.Close()
			return false, err
		}
		found = append(found, [2]string{t, id})
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(found) != 1 {
		return false, err
	}
	err = Match(ctx, tx, line, found[0][0], found[0][1])
	if errs.Is(err, core.ID, "NOT_MATCHABLE") {
		return false, nil // matched by another line already
	}
	return err == nil, err
}

// Document tries the lines of a document's account that wait with its bank reference (a receipt registered or a
// payment executed after its statement line, BR01).
func Document(ctx context.Context, tx db.Tx, account string, ref *string) error {
	if ref == nil {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM statement_line WHERE bank_account_id = $1 AND bank_reference = $2
		AND state = 'unmatched'`, account, *ref)
	if err != nil {
		return err
	}
	var lines []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		lines = append(lines, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := ByReference(ctx, tx, l); err != nil {
			return err
		}
	}
	return nil
}
