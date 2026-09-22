# Security policy

## Reporting a vulnerability

Please report security issues privately, through GitHub's private
vulnerability reporting on this repository: open the **Security** tab and
choose **Report a vulnerability**. That opens a private thread visible only
to the maintainers.

Please do not open a public issue, and please do not post details in a pull
request or a discussion until a fix is available.

Expect a first response within one week. If a report is valid we will agree
a disclosure timeline with you before publishing anything.

## In scope

- The `agentpulse` command-line program in this repository.
- The release artifacts it publishes: the archives, their checksums, and the
  Homebrew and PyPI packages built from them.
- `install.sh`.

## Out of scope here

The AgentPulse iPhone app and the hosted relay are not in this repository.
If you have found something in either, report it the same way — through this
repository's private vulnerability reporting — and say which component it
concerns; it will be routed.

## Good to know

The bridge sends a closed set of structured events and never transmits file
paths, command text, tool output, or prompt text. A way to get any of those
off a user's machine through this program is a vulnerability, and we would
very much like to hear about it.
