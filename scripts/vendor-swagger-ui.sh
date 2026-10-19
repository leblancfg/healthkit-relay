#!/usr/bin/env bash
# Vendors swagger-ui-dist into server/swaggerui/ for /docs (embedded in the binary: no CDN at
# run time). Downloads the npm tarball, checks it against the integrity hash pinned below
# (npm's sha512, from https://registry.npmjs.org/swagger-ui-dist), and copies the few files
# /docs serves. To update: change VERSION and INTEGRITY, run it, check /docs, commit.
#
# Needs bash, curl, tar, sha512sum, base64, xxd or od.
set -euo pipefail

VERSION=5.33.0
INTEGRITY=sha512-wpdK+m6BU5yj6pmUdMskZVTSWYG4DLglAx3sIhylloY37i8O37IrH+YEpqdXNfpaTGxILRBFzUqLF2jKqbfI7A==
FILES=(swagger-ui-bundle.js swagger-ui.css favicon-32x32.png LICENSE NOTICE)

root=$(cd "$(dirname "$0")/.." && pwd)
dest=$root/server/swaggerui
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

curl -fsSL -o "$work/pkg.tgz" "https://registry.npmjs.org/swagger-ui-dist/-/swagger-ui-dist-$VERSION.tgz"
got="sha512-$(sha512sum "$work/pkg.tgz" | cut -d' ' -f1 | xxd -r -p | base64 -w0)"
if [ "$got" != "$INTEGRITY" ]; then
  echo "integrity mismatch: got $got, want $INTEGRITY" >&2
  exit 1
fi
mkdir "$work/x"
tar -xzf "$work/pkg.tgz" -C "$work/x"
for f in "${FILES[@]}"; do
  cp "$work/x/package/$f" "$dest/$f"
done
{
  echo "swagger-ui-dist $VERSION ($INTEGRITY)"
  (cd "$dest" && sha256sum "${FILES[@]}")
} > "$dest/SHA256SUMS"
echo "vendored swagger-ui-dist $VERSION into $dest"
