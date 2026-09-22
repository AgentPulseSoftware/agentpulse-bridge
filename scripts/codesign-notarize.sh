#!/bin/sh
# codesign-notarize.sh signs and notarizes one macOS binary as a GoReleaser
# build.hooks.post step (SEC-10, BR-20). It is a no-op unless SIGN=1, so an
# unsigned build (the only kind this repository can currently prove, since
# no Developer ID certificate exists yet) is unaffected and still works.
#
# Usage: codesign-notarize.sh <path-to-binary> <goos>
#
# Required environment when SIGN=1 (all read from GitHub Actions secrets,
# never checked into this repository, per SEC-03):
#   APPLE_CODESIGN_IDENTITY   - the Developer ID Application certificate's
#                               identity (as `security find-identity` prints
#                               it), already imported into the CI runner's
#                               keychain by an earlier workflow step. This
#                               script never creates, imports into, or
#                               touches a keychain itself.
#   APPLE_API_KEY_ID          - App Store Connect API key ID
#   APPLE_API_ISSUER_ID       - App Store Connect API issuer ID
#   APPLE_API_KEY_PATH        - path to the API key's .p8 file, written to a
#                               runner temp file by the workflow, never to
#                               this repository
#
# What it does:
#   1. codesign --options runtime --timestamp, so Gatekeeper can check the
#      binary online even without a staple.
#   2. Zips the signed binary (notarytool only accepts zip/pkg/dmg) and
#      submits it with notarytool, waiting for a result.
#   3. Attempts `xcrun stapler staple` on the binary. Apple documents
#      stapling for .app bundles, installer packages, and disk images; it
#      is not documented as supported for a bare command-line executable
#      like this one, so a staple failure here is logged and tolerated,
#      not fatal — the notarization ticket itself is still recorded with
#      Apple and Gatekeeper's online check still passes. This distinction
#      is unverified against a real certificate; a certificate and a real
#      notarization run are needed to confirm it.
set -eu

bin_path="${1:?usage: codesign-notarize.sh <path-to-binary> <goos>}"
goos="${2:?usage: codesign-notarize.sh <path-to-binary> <goos>}"

if [ "${SIGN:-}" != "1" ]; then
	# Unsigned build: the default until a Developer ID certificate exists.
	exit 0
fi

if [ "$goos" != "darwin" ]; then
	# Linux binaries are never signed or notarized.
	exit 0
fi

: "${APPLE_CODESIGN_IDENTITY:?SIGN=1 requires APPLE_CODESIGN_IDENTITY}"
: "${APPLE_API_KEY_ID:?SIGN=1 requires APPLE_API_KEY_ID}"
: "${APPLE_API_ISSUER_ID:?SIGN=1 requires APPLE_API_ISSUER_ID}"
: "${APPLE_API_KEY_PATH:?SIGN=1 requires APPLE_API_KEY_PATH}"

echo "codesign-notarize: signing ${bin_path}"
codesign --sign "$APPLE_CODESIGN_IDENTITY" --options runtime --timestamp --force "$bin_path"

zip_path="${bin_path}.notarize.zip"
trap 'rm -f "$zip_path"' EXIT
rm -f "$zip_path"
zip -q -j "$zip_path" "$bin_path"

echo "codesign-notarize: submitting ${zip_path} to notarytool"
xcrun notarytool submit "$zip_path" \
	--key "$APPLE_API_KEY_PATH" \
	--key-id "$APPLE_API_KEY_ID" \
	--issuer "$APPLE_API_ISSUER_ID" \
	--wait

echo "codesign-notarize: attempting staple (best effort; see script header)"
if ! xcrun stapler staple "$bin_path"; then
	echo "codesign-notarize: staple not applied (expected for a bare executable); notarization ticket is still recorded with Apple"
fi
