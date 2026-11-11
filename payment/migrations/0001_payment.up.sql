-- erp/payment's tables (docs/design.md, "拥有的数据"). The runtime role reads and writes them; the migration creates
-- no role and grants nothing (protocol 19).

-- One queued realignment of the department projection at a time (internal/orgunits): an event of a newer mdm/org
-- epoch waits for it (protocol 02).
CREATE TABLE org_realign (
    single    boolean PRIMARY KEY DEFAULT true CHECK (single),
    queued_at timestamptz NOT NULL DEFAULT now()
);

-- Every incremental fact this component published (fund and accounting facts), by its ID: what FactService.Read
-- answers (protocol 05, common rules: a consumer that missed one reads it back).
CREATE TABLE payment_fact (
    fact_id     uuid NOT NULL,
    type        text NOT NULL,
    data        jsonb NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (fact_id, type)
);

-- A legal entity's company bank account (docs/design.md; internal/bankaccounts). The number is a restricted field.
CREATE TABLE bank_account (
    id              uuid PRIMARY KEY,
    legal_entity_id uuid NOT NULL,
    name            text NOT NULL,
    bank            text NOT NULL,
    number          text NOT NULL,
    currency        text NOT NULL,
    state           text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'inactive')),
    search          text NOT NULL DEFAULT '',
    search_version  int NOT NULL DEFAULT 0,
    version         bigint NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (legal_entity_id, number)
);

-- Money received, registered offline (RC01, RC02; internal/receipts): received when registered, reversed with a
-- reason; the fund fact's ID it published and the reversal's.
CREATE TABLE receipt (
    id               uuid PRIMARY KEY,
    number           text NOT NULL UNIQUE,
    legal_entity_id  uuid NOT NULL,
    bank_account_id  uuid NOT NULL REFERENCES bank_account (id),
    payer_id         uuid,
    currency         text NOT NULL,
    amount           numeric NOT NULL CHECK (amount > 0),
    received_on      date NOT NULL,
    bank_reference   text NOT NULL,
    order_id         uuid,
    description      text,
    state            text NOT NULL DEFAULT 'received' CHECK (state IN ('received', 'reversed')),
    fact_id          uuid NOT NULL,
    reversal_fact_id uuid,
    maker_id         text NOT NULL,
    reason           text,
    search           text NOT NULL DEFAULT '',
    search_version   int NOT NULL DEFAULT 0,
    version          bigint NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
-- RC01: a bank reference once per account among the receipts not reversed
CREATE UNIQUE INDEX receipt_bank_reference ON receipt (bank_account_id, bank_reference) WHERE state = 'received';
CREATE INDEX receipt_listed ON receipt (legal_entity_id, created_at DESC, id);

-- A payment request (PR01-PR08; internal/requests): against erp/finance's open items, each line's claim made when it
-- is submitted and confirmed when it is released; the payment registered when executed. department_path copies its
-- department's path (internal/orgunits rewrites it when the department moves).
CREATE TABLE payment_request (
    id              uuid PRIMARY KEY,
    number          text NOT NULL UNIQUE,
    legal_entity_id uuid NOT NULL,
    kind            text NOT NULL CHECK (kind IN ('payment', 'refund')),
    payee_id        uuid NOT NULL,
    bank_account_id uuid NOT NULL REFERENCES bank_account (id),
    currency        text NOT NULL,
    amount          numeric NOT NULL CHECK (amount > 0),
    purpose         text NOT NULL,
    reference       text,
    department_id   uuid,
    department_path text,
    state           text NOT NULL DEFAULT 'draft'
        CHECK (state IN ('draft', 'submitted', 'approved', 'ready', 'paid', 'returned', 'cancelled')),
    maker_id        text NOT NULL,
    applicant_id    text,
    approver_id     text,
    releaser_id     text,
    paid_amount     numeric,
    paid_on         date,
    bank_reference  text,
    fact_id         uuid,
    reversal_fact_id uuid,
    reason          text,
    search          text NOT NULL DEFAULT '',
    search_version  int NOT NULL DEFAULT 0,
    version         bigint NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX payment_request_listed ON payment_request (legal_entity_id, created_at DESC, id);

CREATE TABLE payment_request_line (
    id               uuid PRIMARY KEY,
    request_id       uuid NOT NULL REFERENCES payment_request (id),
    position         int NOT NULL,
    open_item_id     uuid,
    amount           numeric NOT NULL CHECK (amount > 0),
    claim_id         uuid,
    claim_expires_at timestamptz,
    paid             numeric NOT NULL DEFAULT 0,
    UNIQUE (request_id, position)
);
