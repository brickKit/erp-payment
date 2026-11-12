-- Approvals on the SDK's flow kernel (be-protocol chapter 17; its tables come with the SDK's migrations): the legal
-- entity's payment policy names the approver, a position of mdm/org or one principal (at most one); the documents keep
-- their legal entity's time zone, so an approval decides who holds the position on the legal entity's business date
-- inside its transaction; an entry keeps the exchange rate taken when it was submitted, its posting being in the
-- decision's transaction.
CREATE TABLE payment_policy (
    legal_entity_id      uuid PRIMARY KEY,
    approver_position_id uuid,
    approver_principal   text,
    version              bigint NOT NULL DEFAULT 1,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payment_policy_one_approver CHECK (approver_position_id IS NULL OR approver_principal IS NULL)
);
ALTER TABLE payment_request ADD COLUMN timezone text NOT NULL DEFAULT 'UTC';
ALTER TABLE bank_entry ADD COLUMN timezone text NOT NULL DEFAULT 'UTC';
ALTER TABLE bank_entry ADD COLUMN rate jsonb;
