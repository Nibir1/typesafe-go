#!/usr/bin/env bash
#
# Cut a release: verify, tag the root, re-point the submodules, tag those.
#
#   make release                      # dry run, shows exactly what would happen
#   make release VERSION=v1.0.0       # dry run for a specific version
#   make release VERSION=v1.0.0 CONFIRM=yes
#
# The release body comes from Release_Notes.md — the section whose heading
# matches the version. What is written there is what people see.
#
# Dry run is the default on purpose. Pushing a tag is not undoable: the Go
# module proxy fetches and caches it within minutes, and a version that has
# been published stays published even if the tag is deleted. `git tag -d` does
# not unpublish anything.
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

readonly NOTES="Release_Notes.md"
readonly REMOTE="${REMOTE:-origin}"

# Submodules, in dependency order. nethttp is imported by the three framework
# integrations, so it has to resolve before they can.
readonly SUBMODULES=(
  lint
  typesafecache
  typesafeotel
  typesafeprom
  integrations/nethttp
  integrations/gin
  integrations/echo
  integrations/fiber
  integrations/langchaingo
  integrations/temporal
  integrations/mcp
)

if [[ -t 1 ]]; then
  readonly B=$'\033[1m' R=$'\033[31m' G=$'\033[32m' Y=$'\033[33m' D=$'\033[2m' O=$'\033[0m'
else
  readonly B='' R='' G='' Y='' D='' O=''
fi

step()  { printf '\n%s==> %s%s\n' "$B" "$1" "$O"; }
ok()    { printf '  %s✓%s %s\n' "$G" "$O" "$1"; }
warn()  { printf '  %s!%s %s\n' "$Y" "$O" "$1"; }
die()   { printf '  %s✗ %s%s\n' "$R" "$1" "$O" >&2; exit 1; }
note()  { printf '  %s%s%s\n' "$D" "$1" "$O"; }

# would runs a command, or prints it when this is a dry run.
would() {
  if [[ "$CONFIRM" == "yes" ]]; then
    "$@"
  else
    printf '  %swould run:%s %s\n' "$D" "$O" "$*"
  fi
}

# --- inputs -------------------------------------------------------------------

VERSION="${VERSION:-}"
CONFIRM="${CONFIRM:-no}"

# With no version, take the newest section in the notes. Writing the notes
# first and deriving the version from them means the two cannot disagree.
if [[ -z "$VERSION" ]]; then
  VERSION="$(grep -m1 -oE '^## v[0-9]+\.[0-9]+\.[0-9]+[^ ]*' "$NOTES" | sed 's/^## //' || true)"
  [[ -n "$VERSION" ]] || die "no version given and no '## vX.Y.Z' heading in $NOTES"
  note "version taken from $NOTES: $VERSION"
fi

[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] \
  || die "$VERSION is not a semantic version tag (want vX.Y.Z)"

# --- the release notes --------------------------------------------------------

step "Release notes"

[[ -f "$NOTES" ]] || die "$NOTES does not exist"

# The section from this version's heading to the next one. A release whose
# notes are missing is a release nobody can read.
BODY="$(awk -v ver="## $VERSION" '
  $0 == ver { found = 1; next }
  found && /^## v[0-9]/ { exit }
  found { print }
' "$NOTES")"

[[ -n "${BODY//[[:space:]]/}" ]] \
  || die "$NOTES has no '## $VERSION' section, or it is empty"

BODY_LINES="$(printf '%s\n' "$BODY" | grep -c '' || true)"
ok "found $BODY_LINES lines of notes for $VERSION"
note "first line: $(printf '%s\n' "$BODY" | grep -m1 -v '^[[:space:]]*$' | cut -c1-64)…"

# --- preconditions ------------------------------------------------------------

step "Preconditions"

command -v git > /dev/null || die "git is not installed"
git rev-parse --is-inside-work-tree > /dev/null 2>&1 || die "not a git repository"

if [[ -n "$(git status --porcelain)" ]]; then
  git status --short | head -10
  die "the working tree is not clean; commit or stash first"
fi
ok "working tree is clean"

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [[ "$BRANCH" != "main" ]]; then
  warn "on branch '$BRANCH', not main"
  [[ "$CONFIRM" == "yes" ]] && die "refusing to release from '$BRANCH'; check out main"
fi

if git rev-parse "$VERSION" > /dev/null 2>&1; then
  die "tag $VERSION already exists locally"
fi
if git ls-remote --tags --exit-code "$REMOTE" "refs/tags/$VERSION" > /dev/null 2>&1; then
  die "tag $VERSION already exists on $REMOTE — a published version cannot be replaced"
fi
ok "$VERSION is not taken"

if ! grep -q "\[${VERSION#v}\]" CHANGELOG.md; then
  warn "CHANGELOG.md has no [${VERSION#v}] entry"
  [[ "$CONFIRM" == "yes" ]] && die "write the changelog entry before releasing"
fi

# --- the gate -----------------------------------------------------------------

step "Full offline gate"
note "this is what CI runs; a tag is not the place to discover a failure"
make verify > /tmp/release-verify.log 2>&1 || {
  tail -30 /tmp/release-verify.log
  die "make verify failed — see /tmp/release-verify.log"
}
ok "make verify passed"

# --- what will happen ---------------------------------------------------------

step "Plan for $VERSION"

cat <<PLAN
  1. tag and push $VERSION            (the root module)
  2. wait for proxy.golang.org        (it must serve $VERSION before step 3)
  3. make release-prep VERSION=$VERSION
     — strips the development replaces and pins the real version, because Go
       ignores a replace in a dependency's go.mod and a submodule shipped with
       one is a module nobody can build
  4. go mod tidy + test in each of the ${#SUBMODULES[@]} submodules
  5. commit, then tag and push each submodule as <module>/$VERSION
  6. make release-revert VERSION=$VERSION  (back to development)

  CI takes over on the pushed tag: it re-verifies, runs govulncheck over every
  module, builds the CLI binaries, signs them with keyless Sigstore, attaches
  an SBOM and attests build provenance. The release body is the $VERSION
  section of $NOTES.
PLAN

if [[ "$CONFIRM" != "yes" ]]; then
  printf '\n  %sDry run.%s Nothing was changed or pushed.\n' "$B" "$O"
  printf '  To do it for real:\n\n    %smake release VERSION=%s CONFIRM=yes%s\n\n' \
    "$B" "$VERSION" "$O"
  exit 0
fi

# --- the point of no return ---------------------------------------------------

step "Confirm"

printf '  Pushing %s%s%s to %s publishes it permanently.\n' "$B" "$VERSION" "$O" "$REMOTE"
printf '  The Go module proxy caches a version within minutes, and deleting the\n'
printf '  tag afterwards does not unpublish it.\n\n'
printf '  Type the version to continue: '
read -r TYPED
[[ "$TYPED" == "$VERSION" ]] || die "got '$TYPED', expected '$VERSION' — nothing was done"

# --- root tag -----------------------------------------------------------------

step "Tagging the root module"

would git tag -a "$VERSION" -m "$VERSION

$BODY"
would git push "$REMOTE" "$VERSION"
ok "pushed $VERSION"

step "Waiting for the module proxy"
note "the submodules cannot resolve the root until the proxy has served it"

PROXY_OK=no
for attempt in $(seq 1 30); do
  if GOPROXY=https://proxy.golang.org GOFLAGS= \
     go list -m "github.com/nibir1/typesafe-go@$VERSION" > /dev/null 2>&1; then
    PROXY_OK=yes
    ok "proxy.golang.org is serving $VERSION (after ${attempt} check(s))"
    break
  fi
  sleep 10
done

if [[ "$PROXY_OK" != "yes" ]]; then
  warn "the proxy has not served $VERSION after five minutes"
  note "the root tag is published; finish the submodules by hand when it appears:"
  note "  make release-prep VERSION=$VERSION"
  exit 1
fi

# --- submodules ---------------------------------------------------------------

step "Re-pointing the submodules"

python3 scripts/release_prep.py --version "$VERSION"

for module in "${SUBMODULES[@]}"; do
  printf '  %s%s%s\n' "$D" "$module" "$O"
  ( cd "$module" && go mod tidy && go build ./... && go test -count=1 ./... > /dev/null ) \
    || die "$module failed after re-pointing; the root tag is published, fix and retry"
done
ok "all ${#SUBMODULES[@]} submodules build and test against $VERSION"

python3 scripts/check_release_ready.py --version "$VERSION" --allow-dirty \
  || die "release readiness check failed"

step "Committing the pinned go.mod files"
would git commit -am "Pin submodules to $VERSION"
would git push "$REMOTE" HEAD

step "Tagging the submodules"
for module in "${SUBMODULES[@]}"; do
  tag="$module/$VERSION"
  would git tag -a "$tag" -m "$tag"
  would git push "$REMOTE" "$tag"
  ok "$tag"
done

# --- back to development ------------------------------------------------------

step "Restoring development replaces"
python3 scripts/release_prep.py --version "$VERSION" --revert
for module in "${SUBMODULES[@]}"; do
  ( cd "$module" && go mod tidy > /dev/null 2>&1 ) || true
done
would git commit -am "Restore development replaces after $VERSION"
would git push "$REMOTE" HEAD

step "Done"
ok "$VERSION released"
note "CI is building, signing and publishing the artifacts now:"
note "  https://github.com/nibir1/typesafe-go/actions"
note "  https://github.com/nibir1/typesafe-go/releases/tag/$VERSION"
