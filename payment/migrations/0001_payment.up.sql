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
