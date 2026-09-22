package scrub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxLineLength = 200

// staticForbiddenSubstrings are leak signatures that don't depend on
// anything seen during this run: absolute Unix paths, home-relative paths,
// Windows paths (both the drive-letter and UNC-ish backslash forms), file
// URLs, and a handful of common system directories that show up in real
// tool output (compiler include paths, package caches, dev-container
// mounts) but never in a scrubbed fixture.
var staticForbiddenSubstrings = []string{
	"/Users/", "/home/", "~/",
	`C:\`, `\Users\`,
	"file://",
	"/opt/", "/var/", "/workspaces/",
}

// genericPathPattern is a heuristic backstop for a path this package didn't
// think to name explicitly: two or more path segments (Unix or Windows
// separators) ending in a file extension, e.g. "src/foo.py" or
// "a\b\c.txt". It intentionally requires at least one separator (so a
// bare word with a dot, like a version number "v0.1.0", doesn't trip it)
// and a short alphanumeric extension.
var genericPathPattern = regexp.MustCompile(`[A-Za-z0-9_.-]+[/\\][A-Za-z0-9_.-]+\.[A-Za-z0-9]{1,10}\b`)

// ourOwnPlaceholderPattern matches scrub's own "/scrubbed/..." replacement
// values (a path, a project-cwd hash, or the fixed transcript path). These
// are the intended, safe output of scrubbing a path field and would
// otherwise trip genericPathPattern (e.g. "/scrubbed/f1.py" itself has the
// "segment/segment.ext" shape); they're stripped out before that check
// runs so scrub doesn't fail its own self-check on its own placeholders.
var ourOwnPlaceholderPattern = regexp.MustCompile(`/scrubbed/\S*`)

// selfCheck walks every string value in every scrubbed JSON file under
// readDir — after JSON-decoding, so a string's own "\n" escapes become real
// line breaks first (measuring content lines inside string values, not
// JSON file lines) — and fails loudly if any content line:
// contains one of the forbidden substrings above, a cwd value seen during
// this run, or a path matching genericPathPattern; or is longer than 200
// characters. This is the backstop that makes scrub refuse to report
// success over output that might still carry a real path.
//
// Violations name the file as reportDir/<name> rather than readDir/<name>.
// ScrubDir passes its temporary scratch directory as readDir (that's where
// the bytes being checked actually live) but the original recording
// directory as reportDir: readDir is deleted the moment self-check fails
// so a message pointing at it would send the operator to
// a path that no longer exists, while the input file — never modified,
// already on their own machine — is still there to open.
func (c *context) selfCheck(readDir, reportDir string) error {
	entries, err := os.ReadDir(readDir)
	if err != nil {
		return fmt.Errorf("self-check: reading %s: %w", readDir, err)
	}

	forbidden := append([]string{}, staticForbiddenSubstrings...)
	for cwd := range c.cwdsSeen {
		if cwd != "" {
			forbidden = append(forbidden, cwd)
		}
	}

	var violations []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(readDir, e.Name())
		reportPath := filepath.Join(reportDir, e.Name())
		data, err := os.ReadFile(path) //nolint:gosec // scrub's own output directory
		if err != nil {
			return fmt.Errorf("self-check: reading %s: %w", path, err)
		}

		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("self-check: %s is not valid JSON: %w", reportPath, err)
		}

		walkStrings(doc, func(s string) {
			for i, contentLine := range strings.Split(s, "\n") {
				lineNo := i + 1

				// Checked against scrub's own placeholders removed, so a
				// legitimate "/scrubbed/f1.py" or "/scrubbed/<hash>"
				// doesn't fail its own self-check.
				withoutOurPlaceholders := ourOwnPlaceholderPattern.ReplaceAllString(contentLine, "")

				for _, bad := range forbidden {
					if strings.Contains(withoutOurPlaceholders, bad) {
						violations = append(violations, fmt.Sprintf(
							"%s: a string value's line %d contains %q", reportPath, lineNo, bad))
					}
				}
				if m := genericPathPattern.FindString(withoutOurPlaceholders); m != "" {
					violations = append(violations, fmt.Sprintf(
						"%s: a string value's line %d looks like a file path: %q", reportPath, lineNo, m))
				}
				// Length is checked against the real content, placeholders
				// included: the 200-character cap is about unbounded text
				// in general, not specifically about paths.
				if len(contentLine) > maxLineLength {
					violations = append(violations, fmt.Sprintf(
						"%s: a string value's line %d is %d characters, over the %d limit",
						reportPath, lineNo, len(contentLine), maxLineLength))
				}
			}
		})
	}

	if len(violations) == 0 {
		return nil
	}
	sort.Strings(violations)
	return fmt.Errorf("scrub self-check failed, output may still contain a real path or unbounded text:\n%s",
		strings.Join(violations, "\n"))
}

// walkStrings calls fn with every string value found anywhere in v (a tree
// decoded from JSON via encoding/json into "any": objects become
// map[string]any, arrays []any). Map keys are field names, not content, so
// they are not visited.
func walkStrings(v any, fn func(string)) {
	switch val := v.(type) {
	case string:
		fn(val)
	case map[string]any:
		for _, fv := range val {
			walkStrings(fv, fn)
		}
	case []any:
		for _, e := range val {
			walkStrings(e, fn)
		}
	}
}
