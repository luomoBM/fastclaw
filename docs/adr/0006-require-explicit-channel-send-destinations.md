# Require explicit Channel Send destinations

`channels send` requires the operator to name the channel type, agent, and typed destination; it infers the channel account only when that agent has exactly one matching binding. FastClaw will not default operator-initiated delivery to the latest conversation because unattended scripts could silently send sensitive content to whichever person happened to chat most recently.
