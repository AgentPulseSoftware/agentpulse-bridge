// Package scrub turns a directory of raw recorded Claude Code hook JSON
// documents (SPEC 9.4, 18) into a fixture-safe copy: every value that could
// carry a path, prompt, command, or tool output is replaced with a
// synthetic equivalent that preserves parsing-relevant shape, while
// everything the classifier needs (event names, tool names, counts,
// outcomes) survives.
//
// The scrubber is allow-list based: only fields explicitly named here are
// kept or transformed. A field name from a future Claude Code version that
// this package does not recognize is scrubbed by default. This is
// deliberate; see topLevelPassthroughStrings and freeTextToolInputFields
// below for the two allow-lists.
package scrub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
)

// pathFieldNames are the exact tool_input field names treated as
// path-like (SPEC 7.5); any other field ending in "_path" is also treated
// as path-like.
var pathFieldNames = map[string]bool{
	"file_path":     true,
	"path":          true,
	"notebook_path": true,
}

func isPathField(key string) bool {
	return pathFieldNames[key] || strings.HasSuffix(key, "_path")
}

// freeTextToolInputFields are tool_input fields whose value is free text
// (file contents, search text, prompts) and get replaced with a length-only
// placeholder rather than "scrubbed", so the classifier can still see how
// big the change or query was.
var freeTextToolInputFields = map[string]bool{
	"content":     true,
	"old_string":  true,
	"new_string":  true,
	"pattern":     true,
	"query":       true,
	"url":         true,
	"description": true,
	"prompt":      true,
}

// topLevelPassthroughStrings is the envelope allow-list: hook fields whose
// string value is a structural identifier or a fixed enum, not free text,
// a path, or tool output, and safe to keep verbatim exactly as recorded.
// They are required by the classifier to determine event shape (SPEC 7.2).
// Every other top-level string field is scrubbed by default (the allow-list
// policy). "message", "matcher", and "tool_name" are deliberately NOT in
// this list even though they're also structural fields: unlike the ones
// below, their value isn't drawn from a small fixed set the classifier
// defines outright — "tool_name" and "matcher" can also name an
// operator-installed MCP tool or an arbitrary regex, and "message" is text
// Claude Code chooses at runtime — so they each get their own narrower,
// pattern-based allow-list (scrubMessageValue, scrubMatcherValue,
// scrubToolNameValue) instead of a blanket pass-through.
var topLevelPassthroughStrings = map[string]bool{
	"hook_event_name": true, // e.g. "PreToolUse"
	"session_id":      true, // opaque per-session identifier
	"source":          true, // SessionStart: startup|resume|clear|compact
	"reason":          true, // SessionEnd: clear|logout|prompt_input_exit|other
	"permission_mode": true,
	"tool_use_id":     true,
}

// knownNotificationMessagePrefixes are the Notification hook's own fixed
// lead-in phrases (SPEC 7.2's permission_prompt / idle_prompt), shared with
// internal/classify via internal/claudehooks so both packages key off
// identical text. A message starting with one of these keeps only that
// matched prefix — never whatever Claude Code appended after it (a tool
// name, a question, a summary of what changed), since that part is free
// text this package has no way to vet.
var knownNotificationMessagePrefixes = []string{
	claudehooks.NotificationPermissionPromptPrefix,
	claudehooks.NotificationIdlePromptPrefix,
}

// scrubMessageValue implements the Notification "message" field's
// allow-list: kept only as its matched known prefix, otherwise a
// length-only placeholder like the tool_input free-text fields.
func scrubMessageValue(s string) string {
	for _, prefix := range knownNotificationMessagePrefixes {
		if strings.HasPrefix(s, prefix) {
			return prefix
		}
	}
	return fmt.Sprintf("scrubbed (%d chars)", len([]rune(s)))
}

// knownToolNames are Claude Code's built-in tool names — the values
// "tool_name" and "matcher" are actually allowed to be, per SPEC 7.2's
// PreToolUse/PostToolUse/PermissionRequest payloads and the tool list a
// hook matcher can name. Shared between scrubToolNameValue and
// scrubMatcherValue: they're the same concept (a built-in tool's name),
// just appearing in two different fields.
var knownToolNames = map[string]bool{
	"Bash":            true,
	"Edit":            true,
	"MultiEdit":       true,
	"Write":           true,
	"Read":            true,
	"NotebookEdit":    true,
	"Glob":            true,
	"Grep":            true,
	"LS":              true,
	"WebFetch":        true,
	"WebSearch":       true,
	"AskUserQuestion": true,
	"ExitPlanMode":    true,
	"Task":            true,
}

// mcpToolPrefix identifies a tool name as coming from an MCP (Model Context
// Protocol) server the operator connected — e.g. "mcp__filesystem__read_file".
// The server and tool name after the prefix are operator-chosen and not a
// closed set this package can enumerate, so they're replaced with a single
// placeholder rather than kept or fully redacted to "scrubbed": the
// classifier still learns "this was some MCP tool", just not which one.
const mcpToolPrefix = "mcp__"

// scrubToolNameValue implements the "tool_name" field's allow-list: a
// built-in tool name survives verbatim, an MCP tool name is
// reduced to a constant that still says "this was an MCP tool", and
// anything else — a future built-in tool this package doesn't know about
// yet — is scrubbed by default.
func scrubToolNameValue(s string) string {
	if knownToolNames[s] {
		return s
	}
	if strings.HasPrefix(s, mcpToolPrefix) {
		return "mcp__scrubbed"
	}
	return "scrubbed"
}

// scrubMatcherValue implements the "matcher" field's allow-list: kept only
// if it's the empty matcher (BR-08's own) or a bare known tool name.
// Anything else is scrubbed: a matcher can in principle be an arbitrary
// regular expression an operator wrote themselves, which this package has
// no business assuming is safe to keep.
func scrubMatcherValue(s string) string {
	if s == "" || knownToolNames[s] {
		return s
	}
	return "scrubbed"
}

// Report summarizes one ScrubDir run.
type Report struct {
	FilesScrubbed int
	InDir         string
	OutDir        string
}

// ScrubDir scrubs every "*.json" file in inDir into outDir (created if
// needed), keeping filenames identical, applying a stable per-run mapping
// for path-like fields across all files, writes into a temporary directory
// first, and only moves that into outDir once the self-check (grep for
// /Users/, /home/, file-shaped paths, any recorded cwd, and overlong lines)
// passes over it. On a self-check failure the temporary directory is
// deleted and outDir is left exactly as it was — nothing partially or
// unsafely scrubbed is ever visible at the path the caller asked for.
func ScrubDir(inDir, outDir string) (Report, error) {
	report := Report{InDir: inDir, OutDir: outDir}

	entries, err := os.ReadDir(inDir)
	if err != nil {
		return report, fmt.Errorf("reading %s: %w", inDir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	outParent := filepath.Dir(outDir)
	if err := os.MkdirAll(outParent, 0o755); err != nil {
		return report, fmt.Errorf("creating %s: %w", outParent, err)
	}
	tempOut, err := os.MkdirTemp(outParent, ".agentpulse-scrub-*")
	if err != nil {
		return report, fmt.Errorf("creating a temporary output directory: %w", err)
	}
	// Cleaned up on any failure path below; a no-op once outDir has taken
	// tempOut's place (nothing left there to remove).
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(tempOut)
		}
	}()

	ctx := newContext()
	for _, name := range names {
		inPath := filepath.Join(inDir, name)
		raw, err := os.ReadFile(inPath) //nolint:gosec // operator-controlled recording dir
		if err != nil {
			return report, fmt.Errorf("reading %s: %w", inPath, err)
		}

		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return report, fmt.Errorf("%s is not a JSON object: %w", inPath, err)
		}

		scrubbed := ctx.document(doc)

		out, err := json.MarshalIndent(scrubbed, "", "  ")
		if err != nil {
			return report, fmt.Errorf("marshaling scrubbed %s: %w", name, err)
		}
		out = append(out, '\n')

		outPath := filepath.Join(tempOut, name)
		if err := os.WriteFile(outPath, out, 0o600); err != nil {
			return report, fmt.Errorf("writing %s: %w", outPath, err)
		}
		report.FilesScrubbed++
	}

	if err := ctx.selfCheck(tempOut, inDir); err != nil {
		return report, fmt.Errorf("%w\n(no output was written to %s)", err, outDir)
	}

	if err := os.RemoveAll(outDir); err != nil {
		return report, fmt.Errorf("clearing %s before writing the scrubbed output: %w", outDir, err)
	}
	if err := os.Rename(tempOut, outDir); err != nil {
		return report, fmt.Errorf("moving scrubbed output into %s: %w", outDir, err)
	}
	succeeded = true
	return report, nil
}

// context carries the state that must stay consistent across every file in
// one scrub run: the path-substitution mapping (so the same original path
// always becomes the same "/scrubbed/fN...") and the set of original cwd
// values seen, for the post-run self-check.
type context struct {
	pathMap  map[string]string
	nextPath int
	cwdsSeen map[string]bool
}

func newContext() *context {
	return &context{pathMap: map[string]string{}, cwdsSeen: map[string]bool{}}
}

// document scrubs one parsed hook JSON document.
func (c *context) document(doc map[string]any) map[string]any {
	out := make(map[string]any, len(doc))
	for k, v := range doc {
		switch k {
		case "prompt":
			out[k] = scrubPromptValue(v)
		case "cwd":
			out[k] = c.scrubCwdValue(v)
		case "transcript_path":
			out[k] = "/scrubbed/transcript.jsonl"
		case "message":
			if s, ok := v.(string); ok {
				out[k] = scrubMessageValue(s)
			} else {
				out[k] = v
			}
		case "matcher":
			if s, ok := v.(string); ok {
				out[k] = scrubMatcherValue(s)
			} else {
				out[k] = v
			}
		case "tool_name":
			if s, ok := v.(string); ok {
				out[k] = scrubToolNameValue(s)
			} else {
				out[k] = v
			}
		case "tool_input":
			out[k] = c.scrubGenericField("", v)
		case "tool_response":
			out[k] = scrubResponseField("", v)
		default:
			out[k] = c.scrubTopLevelOther(k, v)
		}
	}
	return out
}

func scrubPromptValue(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	return fmt.Sprintf("scrubbed prompt (%d chars)", len([]rune(s)))
}

func (c *context) scrubCwdValue(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	c.cwdsSeen[s] = true
	sum := sha256.Sum256([]byte(s))
	slug := hex.EncodeToString(sum[:])[:8]
	return "/scrubbed/" + slug
}

// scrubTopLevelOther handles every hook-envelope field that is not prompt,
// cwd, transcript_path, tool_input, or tool_response. A string is kept only
// if it is a known structural/enum field (topLevelPassthroughStrings);
// anything else is scrubbed, and non-string values pass through unchanged.
func (c *context) scrubTopLevelOther(key string, v any) any {
	switch val := v.(type) {
	case string:
		if topLevelPassthroughStrings[key] {
			return val
		}
		return "scrubbed"
	case map[string]any, []any:
		// An unrecognized nested structure at the envelope level: preserve
		// its shape but scrub any string leaves conservatively, the same
		// way tool_input's unknown fields are handled.
		return c.scrubGenericField(key, val)
	default:
		return v // numbers, booleans, null
	}
}

// scrubGenericField is the tool_input walker: it recurses through nested
// objects and arrays, applying rules by each field's own name (path-like,
// "command", the free-text list, or the default "scrubbed"), wherever that
// field appears. The key argument is only consulted when v is a string or
// a bare array element; it is ignored (and irrelevant) when v is an object,
// since an object's own fields are then matched by their own names.
func (c *context) scrubGenericField(key string, v any) any {
	switch val := v.(type) {
	case string:
		switch {
		case isPathField(key):
			return c.scrubPath(val)
		case key == "command":
			return scrubCommand(val)
		case freeTextToolInputFields[key]:
			return fmt.Sprintf("scrubbed (%d chars)", len([]rune(val)))
		default:
			return "scrubbed"
		}
	case map[string]any:
		out := make(map[string]any, len(val))
		for k2, fv := range val {
			out[k2] = c.scrubGenericField(k2, fv)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, e := range val {
			out[i] = c.scrubGenericField(key, e)
		}
		return out
	default:
		return v // numbers, booleans, null
	}
}

// safeExtensionPattern is deliberately wider than the narrower
// `^\.[A-Za-z0-9]{1,8}$` would allow: ".xcodeproj" (9 letters after the
// dot) is a real, common Xcode project extension that must survive, so
// the bound here is {1,9} to fit it while staying tight enough
// to exclude anything that looks like a whole filename or a multi-word
// phrase rather than a short extension.
var safeExtensionPattern = regexp.MustCompile(`^\.[A-Za-z0-9]{1,9}$`)

// extensionHasLetterPattern requires at least one ASCII letter somewhere in
// the extension, so a purely numeric suffix — a Social Security number
// fragment (".123456789"), a date used as a database dump's extension
// (".20260913") — is never kept: those look nothing like a real file
// extension, and length alone doesn't rule them out.
var extensionHasLetterPattern = regexp.MustCompile(`[A-Za-z]`)

// safeExtension returns original's file extension only if it looks like a
// genuine short extension — letters and digits only, a handful of
// characters, with at least one letter among them — AND original's own
// base name isn't itself a dotfile. filepath.Ext treats a dotfile's whole
// name as its "extension" (Ext(".env.acmebank.production") ==
// ".production", Ext(".npmrc") == ".npmrc"), which would leak the file's
// real name or a meaningful suffix of it; excluding any base name that
// starts with "." closes that off. Anything that doesn't pass becomes no
// extension at all — safer than guessing.
func safeExtension(original string) string {
	base := filepath.Base(original)
	if strings.HasPrefix(base, ".") {
		return ""
	}
	ext := filepath.Ext(base)
	if safeExtensionPattern.MatchString(ext) && extensionHasLetterPattern.MatchString(ext) {
		return ext
	}
	return ""
}

// scrubPath maps original to a stable "/scrubbed/fN[.ext]" replacement, N
// assigned in first-seen order for this run so the same original path
// always maps to the same replacement across every file in the run.
func (c *context) scrubPath(original string) string {
	if mapped, ok := c.pathMap[original]; ok {
		return mapped
	}
	c.nextPath++
	mapped := fmt.Sprintf("/scrubbed/f%d%s", c.nextPath, safeExtension(original))
	c.pathMap[original] = mapped
	return mapped
}

// scrubResponseField is the tool_response walker. Unlike scrubGenericField,
// only "stdout", "stderr", and a bare string value (key == "") get the
// line-filter treatment (filterResponseLines); every other string field is
// replaced with "scrubbed". Numbers and booleans are kept.
func scrubResponseField(key string, v any) any {
	switch val := v.(type) {
	case string:
		if key == "" || key == "stdout" || key == "stderr" {
			return filterResponseLines(val)
		}
		return "scrubbed"
	case map[string]any:
		out := make(map[string]any, len(val))
		for k2, fv := range val {
			out[k2] = scrubResponseField(k2, fv)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, e := range val {
			out[i] = scrubResponseField(key, e)
		}
		return out
	default:
		return v // numbers and booleans kept
	}
}
