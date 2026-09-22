#!/usr/bin/env bash
# install.sh downloads and installs the AgentPulse bridge (BR-20). It is
# published as a release asset and, later, served from the docs site.
#
# Usage:
#   curl -fsSL https://.../install.sh -o install.sh && sh install.sh
#   AGENTPULSE_VERSION=v1.2.3 sh install.sh
#
# AGENTPULSE_VERSION, when set, must be the exact GitHub release tag,
# which is a bare semantic version, for example "v1.2.3".
#
# It never pipes a download into a shell, never eval's anything, verifies
# the SHA-256 checksum before extracting anything, and refuses to run if
# the download host is not HTTPS.
set -euo pipefail

# RELEASE_BASE_URL is the one place this file names the repository. If the
# module path or GitHub organization ever changes, this is the only line
# in this file that needs to change.
RELEASE_BASE_URL="https://github.com/agentpulsesoftware/agentpulse-bridge/releases"

BINARY_NAME="agentpulse"

# Declared here (not "local" inside main) so the EXIT trap can still see it
# after main() returns; set -u would otherwise treat it as unbound once
# main's local scope is gone.
tmp_dir=""

log() {
	printf '%s\n' "$*" >&2
}

fail() {
	log "install.sh: error: $*"
	exit 1
}

case "$RELEASE_BASE_URL" in
https://*) ;;
*) fail "RELEASE_BASE_URL must be HTTPS, got: $RELEASE_BASE_URL" ;;
esac

detect_os() {
	case "$(uname -s)" in
	Darwin) echo "darwin" ;;
	Linux) echo "linux" ;;
	*) fail "unsupported OS: $(uname -s)" ;;
	esac
}

detect_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo "amd64" ;;
	arm64 | aarch64) echo "arm64" ;;
	*) fail "unsupported architecture: $(uname -m)" ;;
	esac
}

sha256_check() {
	# $1 = file to verify, $2 = checksums file (in the same directory,
	# containing one "<hash>  <filename>" line per artifact).
	local file="$1" checksums="$2"
	if command -v shasum >/dev/null 2>&1; then
		(cd "$(dirname "$file")" && grep " $(basename "$file")\$" "$checksums" | shasum -a 256 -c -)
	elif command -v sha256sum >/dev/null 2>&1; then
		(cd "$(dirname "$file")" && grep " $(basename "$file")\$" "$checksums" | sha256sum -c -)
	else
		fail "neither shasum nor sha256sum is available; refusing to install without checksum verification"
	fi
}

main() {
	local os arch requested tag version_num archive_name archive_url checksums_url dest

	os="$(detect_os)"
	arch="$(detect_arch)"
	requested="${AGENTPULSE_VERSION:-latest}"

	if [ "$requested" = "latest" ]; then
		# GitHub's "/releases/latest" redirects to "/releases/tag/<tag>";
		# resolving the tag from that redirect (HTTPS, no eval, no piping
		# to a shell) avoids guessing a filename.
		tag="$(curl -fsSL -o /dev/null -w '%{url_effective}' "${RELEASE_BASE_URL}/latest" | sed -E 's#.*/tag/##')"
		[ -n "$tag" ] || fail "could not resolve the latest release tag"
	else
		tag="$requested"
	fi

	# GoReleaser's archive name_template uses the bare version (no "v"
	# prefix): agentpulse_<version>_<os>_<arch>.tar.gz. The release tag
	# itself keeps the "v" (that's what "download/<tag>/" below needs).
	version_num="${tag#v}"
	archive_name="agentpulse_${version_num}_${os}_${arch}.tar.gz"
	archive_url="${RELEASE_BASE_URL}/download/${tag}/${archive_name}"
	checksums_url="${RELEASE_BASE_URL}/download/${tag}/checksums.txt"

	tmp_dir="$(mktemp -d)"
	trap 'rm -rf "$tmp_dir"' EXIT

	log "install.sh: downloading ${archive_url}"
	curl -fsSL "$archive_url" -o "${tmp_dir}/${archive_name}" ||
		fail "download failed: $archive_url"
	curl -fsSL "$checksums_url" -o "${tmp_dir}/checksums.txt" ||
		fail "download failed: $checksums_url"

	log "install.sh: verifying checksum"
	if ! sha256_check "${tmp_dir}/${archive_name}" "${tmp_dir}/checksums.txt"; then
		rm -f "${tmp_dir}/${archive_name}"
		fail "checksum verification failed; deleted the download; nothing was installed"
	fi

	log "install.sh: extracting"
	tar -xzf "${tmp_dir}/${archive_name}" -C "$tmp_dir" "$BINARY_NAME" ||
		fail "extraction failed"

	if [ -w "/usr/local/bin" ] 2>/dev/null; then
		dest="/usr/local/bin"
	else
		dest="${HOME}/.local/bin"
		mkdir -p "$dest"
	fi

	install -m 0755 "${tmp_dir}/${BINARY_NAME}" "${dest}/${BINARY_NAME}" ||
		fail "could not install to $dest"

	log "install.sh: installed ${dest}/${BINARY_NAME}"
	log "install.sh: run 'agentpulse pair' to connect this machine to the AgentPulse app"
	case ":$PATH:" in
	*":${dest}:"*) ;;
	*) log "install.sh: note: ${dest} is not on your PATH" ;;
	esac
}

main "$@"
