CREATE TABLE image_line_readings (
 id UUID PRIMARY KEY,
 line TEXT NOT NULL UNIQUE,
 tag TEXT NOT NULL,
 digest TEXT NOT NULL,
 registry TEXT NOT NULL,
 read_at TIMESTAMPTZ NOT NULL
);
