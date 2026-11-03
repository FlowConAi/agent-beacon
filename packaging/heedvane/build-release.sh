#!/usr/bin/env bash
# Builds the Beacon binary the Heedvane installer downloads, for the five machines it supports, and
# writes what the installer reads: one gzipped binary per machine, checksums.txt, and the manifest
# heedvane-beacon.json (version, and per machine the file, its url, size and sha256).
#
#   packaging/heedvane/build-release.sh <version> <output dir>
#
# BASE_URL (default: this repository's release download URL for the tag) is what the manifest's urls
# start with; a test points it at a local file server. Nothing here signs or publishes anything.
set -euo pipefail

version="${1:?usage: build-release.sh <version> <output dir>}"
out="${2:?usage: build-release.sh <version> <output dir>}"
case "$version" in
  ''|*[!0-9A-Za-z.+-]*) echo "bad version: $version" >&2; exit 1 ;;
esac

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
repo="${GITHUB_REPOSITORY:-FlowConAi/agent-beacon}"
tag="heedvane-v${version}"
base_url="${BASE_URL:-https://github.com/${repo}/releases/download/${tag}}"
commit="$(git -C "$root" rev-parse --short HEAD 2>/dev/null || echo unknown)"
built="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
module="github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"

# The binary embeds a hooks binary it installs into agents; the Heedvane helper never installs
# Beacon's hooks, so the build embeds the placeholder the Makefile uses for a source build.
[ -f "$root/cli/beacon/internal/embedded/hooks.bin" ] || echo PLACEHOLDER > "$root/cli/beacon/internal/embedded/hooks.bin"

mkdir -p "$out"
out="$(cd "$out" && pwd)"
rm -f "$out"/beacon_* "$out/checksums.txt" "$out/heedvane-beacon.json"

# Node's process.platform-process.arch is the manifest key, so the installer maps nothing.
targets="darwin-arm64:darwin:arm64 darwin-x64:darwin:amd64 linux-x64:linux:amd64 linux-arm64:linux:arm64 win32-x64:windows:amd64"
assets="{}"
for target in $targets; do
  key="${target%%:*}"; rest="${target#*:}"; goos="${rest%%:*}"; goarch="${rest#*:}"
  ext=""; [ "$goos" = windows ] && ext=".exe"
  binary="$out/beacon_${version}_${goos}_${goarch}${ext}"
  ( cd "$root/cli/beacon" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath \
      -ldflags "-s -w -X ${module}.Version=${version} -X ${module}.GitCommit=${commit} -X ${module}.BuildDate=${built}" \
      -o "$binary" . )
  binary_sha="$(shasum -a 256 "$binary" | cut -d' ' -f1)"
  gzip -9 -n -f "$binary"
  file="$(basename "$binary").gz"
  sha="$(shasum -a 256 "$out/$file" | cut -d' ' -f1)"
  bytes="$(wc -c < "$out/$file" | tr -d ' ')"
  assets="$(jq -c --arg key "$key" --arg file "$file" --arg url "$base_url/$file" --arg sha "$sha" --arg binary "$binary_sha" --argjson bytes "$bytes" \
    '. + {($key): {file: $file, url: $url, sha256: $sha, bytes: $bytes, binarySha256: $binary}}' <<<"$assets")"
  echo "built $key ($file, $bytes bytes)"
done

( cd "$out" && shasum -a 256 beacon_*.gz > checksums.txt )
jq -n --arg version "$version" --arg tag "$tag" --arg commit "$commit" --arg built "$built" --argjson assets "$assets" \
  '{version: $version, tag: $tag, commit: $commit, built: $built, assets: $assets}' > "$out/heedvane-beacon.json"
echo "wrote $out/checksums.txt and $out/heedvane-beacon.json"
