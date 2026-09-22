package hooks

import (
	"path/filepath"
	"strings"
)

// binaryName is the bridge's own program name (BR-01).
const binaryName = "agentpulse"

// Owner decides which hook entries in a settings file belong to this
// bridge, and therefore which ones "agentpulse unpair" may remove and
// "agentpulse pair" may replace.
//
// BR-09's ownership rule: removes exactly the hook entries whose command
// begins with the bridge's own binary path or `agentpulse hook`. A plain
// substring match on "agentpulse" would also remove a hook belonging to
// some unrelated tool with a similar name — `/usr/local/bin/my-agentpulse-wrapper hook`, say. The
// rule is instead structural: the command's first word must name this
// bridge and its second word must be exactly "hook".
type Owner struct {
	// BinaryPath is the absolute, symlink-resolved path of the running
	// bridge binary. It may be empty (nothing then matches by path).
	BinaryPath string
}

// NewOwner returns the Owner for a bridge binary at binaryPath.
func NewOwner(binaryPath string) Owner {
	return Owner{BinaryPath: binaryPath}
}

// Owns reports whether command is a hook entry this bridge installed.
//
// A command qualifies when, after dropping a leading `env AGENTPULSE_*=…`
// prefix (the shape the development-only "agentpulse record install"
// writes), its first word is
//
//   - exactly this bridge's own binary path, or
//   - the bare word "agentpulse" (BR-09's second clause, for a hook
//     registered by name and found on PATH), or
//   - an absolute path whose base name is exactly "agentpulse" — so an
//     entry still belongs to us after a package upgrade has moved the
//     binary, which is the common case where the literal path no longer
//     matches and the stale entry would otherwise be left behind pointing
//     at a binary that no longer exists,
//
// and its second word is exactly "hook". Anything else — another tool's
// command that merely mentions the word, a wrapper script with the word in
// its name, a command that runs the binary with a different subcommand —
// is not ours and is never touched.
func (o Owner) Owns(command string) bool {
	words := splitCommandWords(command)
	words = dropEnvPrefix(words)
	if len(words) < 2 || words[1] != "hook" {
		return false
	}
	first := words[0]
	switch {
	case o.BinaryPath != "" && first == o.BinaryPath:
		return true
	case first == binaryName:
		return true
	case filepath.IsAbs(first) && filepath.Base(first) == binaryName:
		return true
	default:
		return false
	}
}

// dropEnvPrefix removes a leading `env NAME=VALUE …` prefix whose variable
// names all start with "AGENTPULSE_", which is how the development-only
// "record install" command passes AGENTPULSE_RECORD_DIR to the hook. Any
// other environment prefix (another tool's, or one setting a variable that
// is not ours) leaves the words untouched, so such a command is not
// recognized as ours.
func dropEnvPrefix(words []string) []string {
	if len(words) == 0 || words[0] != "env" {
		return words
	}
	i := 1
	for i < len(words) {
		name, _, isAssignment := strings.Cut(words[i], "=")
		if !isAssignment || !strings.HasPrefix(name, "AGENTPULSE_") {
			break
		}
		i++
	}
	if i == 1 {
		return words // "env" with no AgentPulse assignment after it: not ours
	}
	return words[i:]
}

// splitCommandWords splits a shell command line into words the way a
// POSIX shell would for the simple forms the bridge writes and recognizes:
// whitespace separates words, single or double quotes group one, and a
// backslash outside a quote escapes the rune after it. It is deliberately
// not a shell parser — it understands no expansion, substitution, or
// operator — because its only job is to answer "what program does this
// command line start, and with what first argument". Anything it cannot
// make sense of simply fails to match, which keeps a hook belonging to
// somebody else: an unpaired quote (the one fail-safe gap left on
// purpose, since covering it needs real shell-grammar error recovery)
// swallows the rest of the line into one word, which then will not equal
// this bridge's own binary path, so Owns returns false rather than a
// false positive. The backslash case is handled; the unpaired-quote
// behavior above is a known, documented limitation.
//
// The backslash case matters in practice: shellQuoteIfNeeded (settings.go)
// writes exactly this escaping for a binary path containing a single
// quote (e.g. "/Users/O'Brien/agentpulse"), so recognizing our own
// previously installed hook entry on a re-pair depends on unescaping it
// the same way.
func splitCommandWords(command string) []string {
	var words []string
	var current strings.Builder
	inWord := false
	var quote rune

	flush := func() {
		if inWord {
			words = append(words, current.String())
			current.Reset()
			inWord = false
		}
	}

	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '\\':
			if i+1 < len(runes) {
				i++
				current.WriteRune(runes[i])
				inWord = true
			}
			// A trailing lone backslash escapes nothing; it is dropped
			// rather than appended literally, matching the doc comment
			// above.
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	flush()
	return words
}
