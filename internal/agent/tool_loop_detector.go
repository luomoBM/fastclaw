package agent

import (
	"crypto/sha256"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

const toolLoopDetectionThreshold = 3

type toolLoopDetector struct {
	last        toolLoopSignature
	consecutive int
}

type toolLoopSignature struct {
	name       string
	inputHash  [32]byte
	resultHash [32]byte
}

func (d *toolLoopDetector) Observe(tc provider.ToolCall, result string) bool {
	sig := toolLoopSignature{
		name:       tc.Function.Name,
		inputHash:  sha256.Sum256([]byte(tc.Function.Arguments)),
		resultHash: sha256.Sum256([]byte(strings.TrimSpace(result))),
	}
	if sig.name == d.last.name && sig.inputHash == d.last.inputHash && sig.resultHash == d.last.resultHash {
		d.consecutive++
	} else {
		d.consecutive = 1
		d.last = sig
	}
	return d.consecutive >= toolLoopDetectionThreshold
}

func repeatedToolCallWarning(content string) provider.Message {
	return provider.Message{Role: "system", Content: content}
}