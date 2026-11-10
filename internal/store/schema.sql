CREATE TABLE schema_migrations (
  version TEXT PRIMARY KEY
);

CREATE TABLE links (
  code TEXT PRIMARY KEY,
  target_url TEXT NOT NULL,
  author_sub TEXT NOT NULL,
  author_email TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE INDEX links_author_created ON links (author_sub, created_at);
CREATE INDEX links_created ON links (created_at);

CREATE TABLE settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  retention_days INTEGER NOT NULL,
  max_creates_per_hour INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
