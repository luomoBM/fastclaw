# Share short-lived DingTalk reply endpoints

DingTalk `sessionWebhook` values will be stored as expiring channel reply endpoints in FastClaw's shared database, keyed by channel, account, and typed target. This lets any replica prefer the temporary reply path without leaking the secret URL into sessions, scheduled jobs, frontend responses, logs, or durable Redis stream payloads; missing or failed endpoints fall back to proactive delivery.
