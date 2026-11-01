
CREATE TABLE maintenance_readings (
    id UUID PRIMARY KEY,
    subject TEXT NOT NULL,
    kind TEXT NOT NULL,
    identity TEXT NOT NULL,
    generation BIGINT NOT NULL DEFAULT 0,
    invalidated BOOLEAN NOT NULL DEFAULT FALSE,
    payload JSONB NOT NULL DEFAULT '{}',
    UNIQUE (subject, kind)
);
