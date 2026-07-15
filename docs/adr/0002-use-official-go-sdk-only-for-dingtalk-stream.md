# Use the official Go SDK only for DingTalk Stream

The DingTalk Channel will use `open-dingtalk/dingtalk-stream-sdk-go` for the persistent Stream protocol while implementing its small required REST surface with an internal HTTP client. This keeps FastClaw a single Go process and avoids both hand-rolling the Stream protocol and importing a broad OpenAPI SDK for a narrow set of endpoints.
