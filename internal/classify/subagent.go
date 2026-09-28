package classify

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// Subagent is the optional `subagent` envelope field (ADR-005 section 1):
// which Claude Code subagent an event happened inside. Both fields are
// bounded so that neither can carry a path, a command, or prose.
type Subagent struct {
	// ID is SubagentID(agent_id): 16 lowercase hex characters.
	ID string `json:"id"`
	// Type is SubagentType(agent_type): 1 to 40 characters from
	// [A-Za-z0-9._:-], or the literal "subagent".
	Type string `json:"type"`
}

const (
	subagentIDLen        = 16
	maxSubagentTypeRunes = 40
	// fallbackSubagentType is sent whenever agent_type is empty or does
	// not fit the pattern once trimmed and cut (ADR-005 section 1).
	fallbackSubagentType = "subagent"
)

// SubagentID returns the first 16 lowercase hex characters of the SHA-256
// digest of Claude Code's agent_id (ADR-005 section 1).
//
// The mapping is one-way: SHA-256 is preimage resistant, so the digest
// gives no practical way back to the agent_id, and keeping only 64 of
// its 256 bits discards even more. The id is used only to tell the
// subagents of one session apart; the raw agent_id never leaves this
// process, and hex digits cannot carry text.
func SubagentID(agentID string) string {
	sum := sha256.Sum256([]byte(agentID))
	return hex.EncodeToString(sum[:])[:subagentIDLen]
}

// SubagentType cleans Claude Code's agent_type for the wire (ADR-005
// sections 1 and 2): surrounding whitespace is trimmed, anything past 40
// characters is cut, and the result is sent only if it matches
// ^[A-Za-z0-9][A-Za-z0-9._:-]{0,39}$. Anything else becomes the literal
// "subagent": a failing value is never escaped, re-encoded, or partly kept.
func SubagentType(agentType string) string {
	t := strings.TrimSpace(agentType)
	if utf8.RuneCountInString(t) > maxSubagentTypeRunes {
		t = string([]rune(t)[:maxSubagentTypeRunes])
	}
	if t == "" || !isAlnum(t[0]) {
		return fallbackSubagentType
	}
	for i := 1; i < len(t); i++ {
		if c := t[i]; !isAlnum(c) && c != '.' && c != '_' && c != ':' && c != '-' {
			return fallbackSubagentType
		}
	}
	return t
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// subagentFor returns the envelope for a hook that fired inside a
// subagent, or nil for the main chain and whenever SubagentsOn is false.
func subagentFor(input HookInput) *Subagent {
	if !input.InSubagent() {
		return nil
	}
	return &Subagent{ID: SubagentID(input.AgentID), Type: SubagentType(input.AgentType)}
}

// InSubagent reports whether this hook call is classified as part of a
// subagent chain: subagent classification is on and the hook carries an
// agent_id (COMPATIBILITY.md: the main session's own hooks never do).
func (in HookInput) InSubagent() bool {
	return in.SubagentsOn && in.AgentID != ""
}

// excludedInSubagent reports whether a hook would produce one of the
// event types ADR-005 section 1 forbids the `subagent` field on
// (session_start, prompt_submitted, stop, session_end). Such a hook
// inside a subagent emits nothing. project_seen, the fifth, is produced
// by "agentpulse hook" itself and is dropped there.
func excludedInSubagent(hookEventName string) bool {
	switch hookEventName {
	case HookSessionStart, HookUserPromptSubmit, HookStop, HookSessionEnd:
		return true
	default:
		return false
	}
}
