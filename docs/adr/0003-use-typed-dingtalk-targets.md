# Use typed DingTalk targets

DingTalk conversation addresses will be persisted as `user:<staffId>` for direct recipients and `group:<conversationId>` for groups. Explicit target types avoid brittle ID-shape inference and let sessions, delayed replies, and scheduled messages select the correct proactive API without auxiliary routing state.
