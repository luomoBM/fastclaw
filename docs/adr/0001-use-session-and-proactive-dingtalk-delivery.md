# Use session and proactive delivery for DingTalk

The DingTalk Channel will reply through the inbound message's temporary `sessionWebhook` when available and fall back to DingTalk's proactive messaging API for expired sessions and agent-initiated messages. This adds permission and implementation cost, but preserves FastClaw's delayed-reply and scheduled-message semantics instead of making delivery depend on a short-lived callback URL.
