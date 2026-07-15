# FastClaw

FastClaw connects agents to conversations across messaging platforms while preserving consistent routing and session identity.

## Language

**DingTalk Channel**:
A built-in FastClaw bot binding that carries direct and group conversations through DingTalk Stream mode.
_Avoid_: DingTalk plugin, DingTalk webhook

**Group Conversation**:
A messaging-platform group whose members share one agent session while each message retains its sender identity.
_Avoid_: Per-member group session

**DingTalk Target**:
A stable, typed address for DingTalk delivery: `user:<staffId>` for a direct recipient or `group:<conversationId>` for a group.
_Avoid_: Bare DingTalk ID, ID-shape inference

**Channel Send**:
An operator-initiated delivery that sends content directly through a bound messaging channel without starting an agent turn.
_Avoid_: Agent prompt, inbound simulation, cron trigger
