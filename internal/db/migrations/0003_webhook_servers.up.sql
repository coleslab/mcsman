-- Optional per-server scoping for webhooks. all_servers is explicit so
-- that deleting the last scoped server leaves the webhook scoped to
-- nothing rather than silently widening it to every server.
ALTER TABLE webhooks ADD COLUMN all_servers INTEGER NOT NULL DEFAULT 1;
CREATE TABLE webhook_servers (
  webhook_id TEXT NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
  server_id  TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  PRIMARY KEY (webhook_id, server_id)
);
CREATE INDEX idx_webhook_servers_server ON webhook_servers(server_id);
