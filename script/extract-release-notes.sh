#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Copyright (C) 2026 SukramJ
#
# Extract the changelog.md section for the given version and emit it
# as a self-contained release-notes payload on stdout. Single source
# of truth shared by `make release-notes` (local dry-run) and the
# .github/workflows/release-on-tag.yml workflow.
#
# Uses only POSIX-compatible awk + sed so it runs the same on macOS
# (BSD awk) and Ubuntu (gawk) — no `match($0, regex, array)` tricks.
#
# Usage: script/extract-release-notes.sh <version>
#
# Exits non-zero when no matching section is found, so `make release`
# fails fast instead of producing an empty release body.
#
# The payload is also size-capped: GitHub rejects a release body over
# 125000 characters with a 422, which the release workflow would only
# hit *after* pushing the tag. Over the cap the notes are truncated on
# a line boundary and a pointer to changelog.md is appended. Override
# the cap with MAX_BODY_BYTES when verifying that path locally.

set -euo pipefail

if [ $# -lt 1 ]; then
	echo "usage: $0 <version>" >&2
	exit 2
fi

VERSION="$1"
CHANGELOG="${CHANGELOG:-changelog.md}"

if [ ! -f "$CHANGELOG" ]; then
	echo "error: $CHANGELOG not found at $(pwd)" >&2
	exit 1
fi

# Body: skip the header line itself, print everything until the next
# "# Version " header (or EOF).
body=$(awk -v ver="$VERSION" '
	/^# Version / {
		if (insec) exit
		if ($0 ~ "^# Version " ver " ") { insec=1; next }
	}
	insec { print }
' "$CHANGELOG")

if [ -z "$body" ]; then
	echo "error: no '# Version $VERSION ' section found in $CHANGELOG" >&2
	exit 1
fi

# Previous version: the next "# Version <tag> ..." header that appears
# after our section. Splitting the regex/extraction into awk+sed keeps
# us off the gawk-only match-with-array form.
prev_header=$(awk -v ver="$VERSION" '
	$0 ~ "^# Version " ver " " { insec=1; next }
	insec && /^# Version / { print; exit }
' "$CHANGELOG")

prev_version=""
if [ -n "$prev_header" ]; then
	prev_version=$(printf '%s\n' "$prev_header" | sed -E 's/^# Version ([^ ]+).*$/\1/')
fi

repo="${GITHUB_REPOSITORY:-SukramJ/go-zendure2mqtt}"

# Assemble the payload in memory: the body, then optionally the compare
# link. The first release has no predecessor — that's fine, just skip
# the link.
printf -v payload '%s\n' "$body"

if [ -n "$prev_version" ]; then
	# Release tags follow the vX.Y.Z convention, but changelog headers and
	# the VERSION argument are bare (X.Y.Z). Prefix both sides so the compare
	# link points at real tags instead of non-existent bare refs.
	printf -v compare_link '\n**Full Changelog**: https://github.com/%s/compare/v%s...v%s\n' \
		"$repo" "$prev_version" "$VERSION"
	payload="${payload}${compare_link}"
fi

# GitHub rejects a release body over 125000 characters with a 422 — and
# the workflow only gets there *after* the tag is already on the remote,
# so an oversized body leaves a pushed tag with no release at all. Trim
# instead, and point at the full section in changelog.md.
#
# The cap is the raw API limit, not a reduced one: release-on-tag.yml
# does not set `generate_release_notes`, so GitHub appends nothing to
# this body. Counting bytes against a character limit is conservative
# on its own (a multi-byte character costs more bytes than characters),
# which is the direction we want.
MAX_BODY_BYTES="${MAX_BODY_BYTES:-125000}"

payload_bytes=$(printf '%s' "$payload" | wc -c | tr -d '[:space:]')

if [ "$payload_bytes" -le "$MAX_BODY_BYTES" ]; then
	printf '%s' "$payload"
	exit 0
fi

echo "warning: release notes for $VERSION are ${payload_bytes} bytes," \
	"over the ${MAX_BODY_BYTES}-byte GitHub release-body cap — truncating" >&2

printf -v footer '\n_Release notes truncated — see the full `# Version %s` section in [changelog.md](https://github.com/%s/blob/v%s/changelog.md)._\n' \
	"$VERSION" "$repo" "$VERSION"

footer_bytes=$(printf '%s' "$footer" | wc -c | tr -d '[:space:]')
budget=$((MAX_BODY_BYTES - footer_bytes))

# Cut on a line boundary so a multi-byte character is never split in
# half (`head -c` would happily do exactly that). LC_ALL=C makes awk's
# length() count bytes rather than characters.
printf '%s' "$payload" | LC_ALL=C awk -v budget="$budget" '
	{
		n = length($0) + 1
		if (total + n > budget) exit
		total += n
		print
	}
'

printf '%s' "$footer"
