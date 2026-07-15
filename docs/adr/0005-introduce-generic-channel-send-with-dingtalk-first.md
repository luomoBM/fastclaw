# Introduce generic Channel Send with DingTalk first

FastClaw will expose operator-initiated delivery through the generic `channels send` command while initially implementing only the DingTalk transport. This preserves a stable CLI surface for future channels without expanding the first release into migrations or behavioral changes for existing channel-specific commands; unsupported channel types fail explicitly.
