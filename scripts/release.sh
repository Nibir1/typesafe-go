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

# Submodules, in two tiers.
#
# TIER1 depends only on the root, which is published before any of this runs.
# TIER2 imports integrations/nethttp, so nethttp's tag has to exist on the
# remote before gin, echo and fiber can even resolve their own go.mod.
#
# The first version of this script kept one flat list, pinned everything,
# tested everything, and only tagged at the very end. gin failed with
# "unknown revision integrations/nethttp/v1.0.0" — correctly, because that tag
# did not exist yet. The comment above the list said nethttp had to be tagged
# first; the code did not do it.
readonly TIER1=(
  lint
  typesafecache
  typesafeotel
  typesafeprom
  integrations/nethttp
  integrations/langchaingo
  integrations/temporal
  integrations/mcp
)
readonly TIER2=(
  integrations/gin
  integrations/echo
  integrations/fiber
)
readonly SUBMODULES=("${TIER1[@]}" "${TIER2[@]}")

# Never tagged, so their replaces never reach a consumer — but they live in the
# repository and CI builds them, and `deploy/example` replaces three submodules
# with local directories. Pinning those submodules raises the version this
# module resolves, so its own `require` lines go stale and it stops building.
# The first release left it that way: eight tags were pushed, and the CI run on
# each of them was red at "Deploy example builds" and "Licence audit" for a
# module nobody ships. Every commit has to build, including the ones made
# halfway through a release.
readonly UNPUBLISHED=(examples deploy/example)

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

# RESUME=submodules picks up after the root tag is already published.
#
# The root tag is the irreversible half. Once it is on the proxy, re-running
# from the top is both impossible (the tag exists) and wrong (it would re-do
# the one step that cannot be re-done). Anything after it is retryable, so it
# needs to be reachable on its own.
RESUME="${RESUME:-}"

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
  if [[ "$RESUME" == "submodules" ]]; then
    warn "the tree is dirty; on a resume that is expected (pinned go.mod files)"
  else
    git status --short | head -10
    die "the working tree is not clean; commit or stash first"
  fi
else
  ok "working tree is clean"
fi

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [[ "$BRANCH" != "main" ]]; then
  warn "on branch '$BRANCH', not main"
  [[ "$CONFIRM" == "yes" ]] && die "refusing to release from '$BRANCH'; check out main"
fi

if [[ "$RESUME" == "submodules" ]]; then
  git ls-remote --tags --exit-code "$REMOTE" "refs/tags/$VERSION" > /dev/null 2>&1 \
    || die "RESUME=submodules but $VERSION is not on $REMOTE; run the full release"
  ok "$VERSION is already published; resuming at the submodules"
else
  if git rev-parse "$VERSION" > /dev/null 2>&1; then
    die "tag $VERSION already exists locally"
  fi
  if git ls-remote --tags --exit-code "$REMOTE" "refs/tags/$VERSION" > /dev/null 2>&1; then
    die "tag $VERSION already exists on $REMOTE — a published version cannot be replaced.
     If the root tag is published and only the submodules are missing, resume with:
       make release VERSION=$VERSION CONFIRM=yes RESUME=submodules"
  fi
  ok "$VERSION is not taken"
fi

if ! grep -q "\[${VERSION#v}\]" CHANGELOG.md; then
  warn "CHANGELOG.md has no [${VERSION#v}] entry"
  [[ "$CONFIRM" == "yes" ]] && die "write the changelog entry before releasing"
fi

# --- the gate -----------------------------------------------------------------

# A resume runs the gate against a tree an interrupted release may have left
# half-repointed: go.mod pinned to a version whose entry is not in go.sum yet,
# because the tidy that would have written it is a step the gate comes before.
# The gate then fails on the exact state the resume exists to finish, and there
# is no way forward from inside the script.
#
# So on a resume, tidy first. It only ever completes what the re-pointing
# started, and the gate still runs on the result — which is the tree that is
# about to be committed and tagged.
if [[ "$RESUME" == "submodules" ]]; then
  step "Tidying what the interrupted release left"
  for module in "${SUBMODULES[@]}" "${UNPUBLISHED[@]}"; do
    ( cd "$module" && go mod tidy > /dev/null 2>&1 ) || true
  done
  ok "${#SUBMODULES[@]} submodule(s) and ${#UNPUBLISHED[@]} unpublished module(s) tidied"
fi

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

if [[ "$RESUME" == "submodules" ]]; then
  printf '  Resuming %s%s%s: the root tag is already published. This will tag\n' "$B" "$VERSION" "$O"
  printf '  the %d submodules, which is also permanent.\n\n' "${#SUBMODULES[@]}"
else
  printf '  Pushing %s%s%s to %s publishes it permanently.\n' "$B" "$VERSION" "$O" "$REMOTE"
fi
printf '  The Go module proxy caches a version within minutes, and deleting the\n'
printf '  tag afterwards does not unpublish it.\n\n'
printf '  Type the version to continue: '
read -r TYPED
[[ "$TYPED" == "$VERSION" ]] || die "got '$TYPED', expected '$VERSION' — nothing was done"

# --- root tag -----------------------------------------------------------------

if [[ "$RESUME" != "submodules" ]]; then

step "Tagging the root module"

would git tag -a "$VERSION" -m "$VERSION

$BODY"
would git push "$REMOTE" "$VERSION"
ok "pushed $VERSION"

step "Waiting for the module proxy"
note "the submodules cannot resolve the root until the proxy has served it"

# Fifteen minutes, not five. The root was served in under a minute and
# integrations/nethttp took longer than five, which stopped a release that was
# otherwise finished. Waiting costs nothing; stopping halfway costs a resume.
PROXY_OK=no
for attempt in $(seq 1 60); do
  if GOPROXY=https://proxy.golang.org GOFLAGS= \
     go list -m "github.com/nibir1/typesafe-go@$VERSION" > /dev/null 2>&1; then
    PROXY_OK=yes
    ok "proxy.golang.org is serving $VERSION (after ${attempt} check(s))"
    break
  fi
  sleep 15
done

if [[ "$PROXY_OK" != "yes" ]]; then
  warn "the proxy has not served $VERSION after fifteen minutes"
  note "the root tag is published; finish the submodules by hand when it appears:"
  note "  make release-prep VERSION=$VERSION"
  exit 1
fi

# --- submodules ---------------------------------------------------------------

fi

step "Re-pointing the submodules"
note "one tier at a time; a commit must never require a tag that does not exist yet"

# tier_prepare tidies, builds and tests the modules in a tier.
tier_prepare() {
  local module
  for module in "$@"; do
    printf '  %s%s%s\n' "$D" "$module" "$O"
    ( cd "$module" && go mod tidy && go build ./... && go test -count=1 ./... > /dev/null ) \
      || die "$module failed after re-pointing; the root tag is published. Fix, then resume with RESUME=submodules"
  done
}

# commit_if_changed keeps a resume idempotent. Re-running a tier that is
# already committed must not abort on "nothing to commit".
commit_if_changed() {
  local message="$1"
  would git add -A
  if [[ "$CONFIRM" == "yes" ]] && git diff --cached --quiet; then
    note "nothing to commit: $message"
    return 0
  fi
  would git commit -m "$message"
  would git push "$REMOTE" HEAD
}

# tidy_unpublished keeps examples and deploy/example building against whatever
# the submodules currently require, so the commit about to be pushed is one CI
# can go green on.
tidy_unpublished() {
  local module
  for module in "${UNPUBLISHED[@]}"; do
    ( cd "$module" && go mod tidy > /dev/null 2>&1 && go build ./... > /dev/null 2>&1 ) \
      || die "$module does not build after re-pointing; fix it before committing"
    note "$module tidied"
  done
}

# join_modules renders a tier as the --only argument the Python tools take.
join_modules() { local IFS=,; echo "$*"; }

# tier_tag commits nothing; it only tags what is already committed.
tier_tag() {
  local module tag
  for module in "$@"; do
    tag="$module/$VERSION"
    if git rev-parse "$tag" > /dev/null 2>&1; then
      warn "$tag already exists, skipping"
      continue
    fi
    would git tag -a "$tag" -m "$tag"
    would git push "$REMOTE" "$tag"
    ok "$tag"
  done
}

step "Tier 1: modules that depend only on the root"
python3 scripts/release_prep.py --version "$VERSION" --only "$(join_modules "${TIER1[@]}")"
tier_prepare "${TIER1[@]}"
tidy_unpublished

python3 scripts/check_release_ready.py --version "$VERSION" --allow-dirty \
  --only "$(join_modules "${TIER1[@]}")" \
  || die "release readiness check failed for tier 1"

step "Committing tier 1"
commit_if_changed "Pin the independent submodules to $VERSION"

step "Tagging tier 1"
tier_tag "${TIER1[@]}"

step "Waiting for integrations/nethttp on the proxy"
note "gin, echo and fiber cannot resolve their own go.mod until it is served"

NETHTTP_OK=no
for attempt in $(seq 1 60); do
  if GOPROXY=https://proxy.golang.org GOFLAGS= \
     go list -m "github.com/nibir1/typesafe-go/integrations/nethttp@$VERSION" > /dev/null 2>&1; then
    NETHTTP_OK=yes
    ok "the proxy is serving integrations/nethttp@$VERSION (after ${attempt} check(s))"
    break
  fi
  sleep 15
done
[[ "$NETHTTP_OK" == "yes" ]] || die "nethttp is not on the proxy yet; resume with RESUME=submodules once it is"

step "Tier 2: the framework integrations"
python3 scripts/release_prep.py --version "$VERSION" --only "$(join_modules "${TIER2[@]}")"
tier_prepare "${TIER2[@]}"
tidy_unpublished

python3 scripts/check_release_ready.py --version "$VERSION" --allow-dirty \
  || die "release readiness check failed"

step "Committing tier 2"
commit_if_changed "Pin the framework integrations to $VERSION"

step "Tagging tier 2"
tier_tag "${TIER2[@]}"

# --- back to development ------------------------------------------------------

step "Restoring development replaces"
python3 scripts/release_prep.py --version "$VERSION" --revert
for module in "${SUBMODULES[@]}"; do
  ( cd "$module" && go mod tidy > /dev/null 2>&1 ) || true
done
tidy_unpublished
commit_if_changed "Restore development replaces after $VERSION"

step "Done"
ok "$VERSION released"
note "CI is building, signing and publishing the artifacts now:"
note "  https://github.com/nibir1/typesafe-go/actions"
note "  https://github.com/nibir1/typesafe-go/releases/tag/$VERSION"
