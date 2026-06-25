-- Match-safety layer: immutable deposit ledger + human-review queue + the
-- unique partial index that makes exact-amount matching unambiguous and
-- removes the dedupAmount SELECT-then-INSERT race.

-- Immutable ledger of every finalized deposit received. Source of truth --
-- nothing is ever dropped. Idempotent on tx_id (ON CONFLICT DO NOTHING).
CREATE TABLE deposits (
    tx_id           TEXT PRIMARY KEY,
    ccy             TEXT NOT NULL,
    amt             NUMERIC(24, 8) NOT NULL,
    state           TEXT NOT NULL,
    received_at     TIMESTAMP WITH TIME ZONE NOT NULL,
    match_status    TEXT NOT NULL DEFAULT 'unmatched', -- unmatched | matched
    matched_order_id UUID REFERENCES transactions(id) ON DELETE SET NULL,
    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- Human-review queue for deposits that could not be auto-matched
-- (no exact-amount order, or lost a claim race). Operators reconcile via
-- /payment/exceptions.
CREATE TABLE match_exceptions (
    id              BIGSERIAL PRIMARY KEY,
    deposit_tx_id   TEXT NOT NULL REFERENCES deposits(tx_id) ON DELETE CASCADE,
    ccy             TEXT NOT NULL,
    amt             NUMERIC(24, 8) NOT NULL,
    reason          TEXT NOT NULL,        -- no_exact_match | race_lost | ambiguous
    candidates      JSONB,                -- snapshot of pending orders considered
    status          TEXT NOT NULL DEFAULT 'open', -- open | resolved
    resolution      TEXT,
    resolved_at     TIMESTAMP WITH TIME ZONE,
    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE INDEX match_exceptions_status_idx ON match_exceptions (status, created_at);

-- Guarantee: at most one pending order per (currency, amount). With a single
-- deposit address and no memo, amount is the ONLY discriminator, so exact
-- matching against this index is unambiguous. The index is also the atomic
-- guard that removes the dedupAmount TOCTOU race -- CreateTransaction retries
-- via ON CONFLICT on collision. Expired/successful orders free their amount.
CREATE UNIQUE INDEX transactions_pending_amount_uidx
    ON transactions (currency, amount)
    WHERE status = 'pending';
