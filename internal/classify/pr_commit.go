package classify

import "regexp"

// ghPRCreatePattern and gitCommitPattern identify the two Bash commands
// SPEC 7.2 tracks by name, applied to the command's normalized first
// pipeline segment.
var (
	ghPRCreatePattern = regexp.MustCompile(`^gh\s+pr\s+create\b`)
	gitCommitPattern  = regexp.MustCompile(`^git\s+commit\b`)
)

// pullURLPattern recognizes a GitHub pull request URL's number, e.g.
// ".../pull/42", in a `gh pr create` command's tool_response text (SPEC
// 7.2: "response contains a pull request URL").
var pullURLPattern = regexp.MustCompile(`pull/(\d+)`)
