#!/bin/sh
# H1: one build. The same run as H0's (../run-h0/build.sh), with the loop ours: the whole spec and
# an empty repository go into a fresh Docker sandbox, a coding agent on the host builds until it
# declares done, then a review session, in a second sandbox where the repository is read-only,
# lists the issues by severity; while any is high, a fix session fixes the high ones and another
# review follows, at most 5 fix rounds. Both sandboxes may install from anything on allowlist.txt
# and never reach denylist.txt, nor the model proxy: the loop runs on this host and holds the key.
#
#   harness/run-h1/build.sh [SPEC]     default: spec/SPEC.md in this repository
#
#   HEATCTL_MODEL_URL       required: base URL of the model proxy, http://host:port, in Anthropic's
#                           format. The loop talks to it from this host; no sandbox can reach it
#   HEATCTL_MODEL           default claude-sonnet-5-5, as the proxy names it
#   HEATCTL_KEY_COMMAND     host command printing the build key for the proxy; default reads the
#                           macOS Keychain item heatctl-build
#   HEATCTL_MUST_NOT_REACH  host:port pairs, space-separated, that must be refused, such as the
#                           house's broker; kept out of this file
#   HEATCTL_BUILDS          archive root, default ~/heatctl-builds
#   HEATCTL_TIMEOUT         seconds before the run's last session is stopped, default 14400
#   HEATCTL_PULL            sandbox image pull policy, default never: every build of a version
#                           runs on the same image. Use missing for the very first build.
#   HEATCTL_FROM_REPO       start from an earlier run's repository instead of an empty one, and
#                           skip the build session: for trying the review and fix rounds again.
#                           Its committed history is copied; its SPEC.md must match SPEC.
#   HEATCTL_CHECK_ONLY      set to 1 to prepare both sandboxes, check their network rules, and
#                           stop there: no session runs and nothing is spent on the model.
#
# Settings that describe the operator's own network can live in a file outside this repository,
# read first if it exists: HEATCTL_ENV, default ~/.config/heatctl/build.env.
#
# This file is the run, top to bottom. The sandboxes and their network rules are in cage.sh, the
# sessions and the review and fix rounds in loop.py, the record of the run in meta.py. Python
# runs from the environment pinned in uv.lock.
set -eu
die() { echo "build.sh: $*" >&2; exit 1; }
env_file=${HEATCTL_ENV:-$HOME/.config/heatctl/build.env}
[ ! -f "$env_file" ] || . "$env_file"
here=$(cd "$(dirname "$0")" && pwd)
spec=${1:-$here/../../spec/SPEC.md}
model_url=${HEATCTL_MODEL_URL:?set HEATCTL_MODEL_URL to the model proxy, http://host:port}
model=${HEATCTL_MODEL:-claude-sonnet-5-5}
timeout=${HEATCTL_TIMEOUT:-14400}
must_not_reach=${HEATCTL_MUST_NOT_REACH:-}
from=${HEATCTL_FROM_REPO:-}
export SBX_NO_TELEMETRY=1 HEATCTL_MODEL HEATCTL_MODEL_URL PYDANTIC_AI_NO_BANNER=1 PYTHONDONTWRITEBYTECODE=1
model_host=$(python3 -c 'import sys,urllib.parse as u; print(u.urlsplit(sys.argv[1]).hostname or "")' "$model_url")
model_port=$(python3 -c 'import sys,urllib.parse as u; p=u.urlsplit(sys.argv[1]); print(p.port or {"http":80,"https":443}[p.scheme])' "$model_url")
[ -n "$model_host" ] || die "HEATCTL_MODEL_URL has no host: $model_url"

run=h1-$(date -u +%Y%m%dT%H%M%SZ)-$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')
dir=${HEATCTL_BUILDS:-$HOME/heatctl-builds}/$run
repo=$dir/repo
review_sb=$run-review
mark() { python3 -c 'import sys, time; print(sys.argv[1], f"{time.time():.3f}")' "$1" >>"$dir/times.txt"; }
loop() {  # stage
  uv run --frozen --project "$here" python "$here/loop.py" "$1" --archive "$dir" --repo "$repo" --run "$run" \
    --build-sandbox "$run" --review-sandbox "$review_sb" --deadline "$deadline"
}

. "$here/cage.sh"

preflight
if [ -n "$from" ]; then
  mkdir -p "$dir"
  git clone -q "$from" "$repo" && git -C "$repo" remote remove origin
  cmp -s "$spec" "$repo/SPEC.md" || die "SPEC.md in $from differs from $spec"
  echo "$from" >"$dir/from.txt"
else
  mkdir -p "$repo"
  cp "$spec" "$repo/SPEC.md"
  git -C "$repo" init -q
  git -C "$repo" -c user.name=harness -c user.email=harness@localhost add SPEC.md
  git -C "$repo" -c user.name=harness -c user.email=harness@localhost commit -qm "spec"
fi
trap cleanup EXIT
echo "$global" | sed '/^$/d' >"$dir/global-secrets.txt"

mark start
prepare build "$run" "$repo"
mkdir -p "$dir/review-scratch"
if [ -n "${HEATCTL_CHECK_ONLY:-}" ]; then
  prepare review "$review_sb" "$dir/review-scratch" "$repo:ro"
  echo "both sandboxes prepared, their network rules hold; no session run: $dir"
  exit 0
fi
mark build-ready

deadline=$(python3 -c 'import sys, time; print(time.time() + float(sys.argv[1]))' "$timeout")
status=0
[ -n "$from" ] || loop build || status=$?
mark review-prep
[ -f "$dir/timeout" ] || prepare review "$review_sb" "$dir/review-scratch" "$repo:ro"
mark review-ready
loop rounds || true
mark loop-done

network_logs
cleanup
trap - EXIT
mark end

uv run --frozen --project "$here" python "$here/meta.py" "$dir" "$run" "$spec" "$model" "$status"
