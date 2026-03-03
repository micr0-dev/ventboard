CREATE TABLE IF NOT EXISTS posts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    body TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    published_at INTEGER,
    cw_labels_json TEXT NOT NULL DEFAULT '[]',
    classifier_version TEXT NOT NULL,
    classifier_attempts INTEGER NOT NULL DEFAULT 0,
    last_classifier_error TEXT,
    ip_hash TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_posts_status_created_at
    ON posts (status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_posts_ip_hash_created_at
    ON posts (ip_hash, created_at DESC);
