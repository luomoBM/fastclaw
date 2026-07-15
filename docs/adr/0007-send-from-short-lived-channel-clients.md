# Send from short-lived Channel clients

Operator-initiated `channels send` delivery runs in the CLI process by loading the selected binding and constructing a short-lived channel client. It does not require the Gateway, enter the message bus, or invoke an agent; the client may reuse persisted temporary reply endpoints before falling back to the channel's proactive API.
