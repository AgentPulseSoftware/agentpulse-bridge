package classify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// toolInputFields decodes only the tool_input fields this package's
// classification logic needs. Unmarshaling into a named-field struct
// (rather than a generic map) means every other field Claude Code might
// send — file contents, search patterns, URLs, descriptions — is silently
// dropped at decode time and never touched again (BR-19): there is no
// later step that could accidentally read, log, or forward them.
type toolInputFields struct {
	Command      string `json:"command"`
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Path         string `json:"path"`
}

// decodeToolInput best-effort decodes input.ToolInput. A missing or
// malformed tool_input yields the zero value, never an error: classifying
// a hook must never fail because one optional field couldn't be read
// (BR-02).
func decodeToolInput(raw json.RawMessage) toolInputFields {
	var f toolInputFields
	if len(raw) == 0 {
		return f
	}
	_ = json.Unmarshal(raw, &f)
	return f
}

// readPathFor returns the tool_input field SPEC 7.2's read-category tools
// use to name what they read, per tool: Read uses file_path; Glob, Grep,
// and LS use path (the directory or glob root they searched — a
// deliberate, documented reading of "distinct files read" since these
// tools don't always name one specific file); WebFetch and WebSearch have
// no local path at all, so they contribute to files_read only via the
// activity event itself, never to the counter. ok is false when this
// tool/call has nothing to count.
func readPathFor(toolName string, f toolInputFields) (string, bool) {
	switch toolName {
	case "Read":
		return f.FilePath, f.FilePath != ""
	case "Glob", "Grep", "LS":
		return f.Path, f.Path != ""
	default: // WebFetch, WebSearch
		return "", false
	}
}

// editPathFor is readPathFor's edit-category counterpart: NotebookEdit
// names its target with notebook_path; Edit, MultiEdit, and Write all use
// file_path.
func editPathFor(toolName string, f toolInputFields) (string, bool) {
	switch toolName {
	case "NotebookEdit":
		return f.NotebookPath, f.NotebookPath != ""
	default: // Edit, MultiEdit, Write
		return f.FilePath, f.FilePath != ""
	}
}

// hashPath returns the hex SHA-256 digest of p (SPEC 7.2: "distinct files
// read and edited (as SHA-256 hashes of paths, local only)"). p itself is
// never retained by the caller beyond computing this digest.
func hashPath(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])
}

// toolResponseFields decodes the two shapes a Claude Code tool_response
// can take: a bare string, or an object with stdout/stderr fields (the
// shape a Bash tool call's response uses).
type toolResponseFields struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// extractResponseText best-effort extracts the combined text of a
// tool_response for local pattern matching (SPEC 7.5), never storing or
// forwarding it: only derived outcome/count values ever leave this
// function's caller. Handles both a bare JSON string and an
// {stdout, stderr, ...} object; anything else (missing, or a shape neither
// matches) yields "".
func extractResponseText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var f toolResponseFields
	if err := json.Unmarshal(raw, &f); err == nil {
		return f.Stdout + "\n" + f.Stderr
	}
	return ""
}
