CREATE TABLE notification_baseline (
 id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
 recorded_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE notification_items (
 id UUID PRIMARY KEY,
 key TEXT NOT NULL,
 title TEXT NOT NULL,
 detail TEXT NOT NULL,
 href TEXT NOT NULL,
 severity TEXT NOT NULL,
 unknown BOOLEAN NOT NULL,
 stale BOOLEAN NOT NULL,
 urgent BOOLEAN NOT NULL,
 opened_at TIMESTAMPTZ NOT NULL,
 announced BOOLEAN NOT NULL DEFAULT false,
 resolved_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX notification_open_key ON notification_items(key) WHERE resolved_at IS NULL;
CREATE TABLE notification_deliveries (
 id UUID PRIMARY KEY,
 channel TEXT NOT NULL,
 kind TEXT NOT NULL CHECK (kind IN ('opened','resolved','digest','test')),
 item_key TEXT NOT NULL DEFAULT '',
 opened_at TIMESTAMPTZ,
 digest_date TEXT NOT NULL DEFAULT '',
 body JSONB NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('pending','sent','retrying','failed')),
 attempts BIGINT NOT NULL DEFAULT 0,
 reason TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL,
 next_at TIMESTAMPTZ,
 finished_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX notification_event ON notification_deliveries(channel,kind,item_key,opened_at) WHERE kind IN ('opened','resolved');
CREATE UNIQUE INDEX notification_digest ON notification_deliveries(channel,digest_date) WHERE kind='digest';
