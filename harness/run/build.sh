#!/bin/sh
# H0: one build. The whole spec and an empty repository go into a fresh Docker sandbox, Claude
# Code builds until it declares done, then a review session, in a second sandbox where the
# repository is read-only, lists the issues by severity; while any is high, a fix session fixes
# the high ones and another review follows, at most 5 fix rounds. Every review is a fresh
# session that sees no earlier review. Everything the wrapper can see is archived. Both
# sandboxes may install from anywhere on allowlist.txt, and never reach denylist.txt.
#
#   harness/run/build.sh [SPEC]        default: spec/SPEC.md in this repository
#
#   HEATCTL_MODEL_URL       required: base URL of the model proxy, http://host:port. Claude Code
#                           talks to it in Anthropic's format; it is the only private address
#                           a build reaches, by exact host and port
#   HEATCTL_MODEL           default claude-sonnet-5-5, as the proxy names it
#   HEATCTL_SMALL_MODEL     the model Claude Code uses for background tasks, default HEATCTL_MODEL
#   HEATCTL_KEY_COMMAND     host command printing the build key for the proxy; default reads the
#                           macOS Keychain item heatctl-build
#   HEATCTL_MUST_NOT_REACH  host:port pairs, space-separated, that must be refused, such as the
#                           house's broker; kept out of this file
#   HEATCTL_BUILDS          archive root, default ~/heatctl-builds
#   HEATCTL_TIMEOUT         seconds before the build is stopped, default 14400
#   HEATCTL_PULL            sandbox image pull policy, default never: every build of a version
#                           runs on the same image. Use missing for the very first build.
#   HEATCTL_FROM_REPO       start from an earlier run's repository instead of an empty one, and
#                           skip the build session: for trying the review and fix rounds again.
#                           Its committed history is copied; its SPEC.md must match SPEC.
#
# Settings that describe the operator's own network can live in a file outside this repository,
# read first if it exists: HEATCTL_ENV, default ~/.config/heatctl/build.env.
set -eu
die() { echo "build.sh: $*" >&2; exit 1; }
env_file=${HEATCTL_ENV:-$HOME/.config/heatctl/build.env}
[ ! -f "$env_file" ] || . "$env_file"
here=$(cd "$(dirname "$0")" && pwd)
spec=${1:-$here/../../spec/SPEC.md}
model_url=${HEATCTL_MODEL_URL:?set HEATCTL_MODEL_URL to the model proxy, http://host:port}
model=${HEATCTL_MODEL:-claude-sonnet-5-5}
small_model=${HEATCTL_SMALL_MODEL:-$model}
timeout=${HEATCTL_TIMEOUT:-14400}
key_command=${HEATCTL_KEY_COMMAND:-/usr/bin/security find-generic-password -a $USER -s heatctl-build -w}
must_not_reach=${HEATCTL_MUST_NOT_REACH:-}
export SBX_NO_TELEMETRY=1
model_host=$(python3 -c 'import sys,urllib.parse as u; print(u.urlsplit(sys.argv[1]).hostname or "")' "$model_url")
model_port=$(python3 -c 'import sys,urllib.parse as u; p=u.urlsplit(sys.argv[1]); print(p.port or {"http":80,"https":443}[p.scheme])' "$model_url")
[ -n "$model_host" ] || die "HEATCTL_MODEL_URL has no host: $model_url"

run=h0-$(date -u +%Y%m%dT%H%M%SZ)-$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')
dir=${HEATCTL_BUILDS:-$HOME/heatctl-builds}/$run
repo=$dir/repo
now() { python3 -c 'import time; print(f"{time.time():.3f}")'; }

# The only credential a build may use is the build key. A secret shared with every sandbox is
# tolerated only if every host it is sent to is refused from the build's sandbox, which is
# checked once the sandbox exists; a global secret whose hosts are not known stops the run.
# The kit's MCP gateway is reachable from every sandbox and no policy closes it; it serves only
# the MCP servers registered on this host, so none may be.
global=$(sbx secret ls --json | python3 -c '
import sys, json
known = {"anthropic": ["api.anthropic.com"], "openai": ["api.openai.com", "auth.openai.com", "chatgpt.com"],
         "github": ["github.com", "api.github.com"]}
j = json.load(sys.stdin)
custom = [c for c in j.get("custom_secrets", []) if c.get("scope") == "global"]
by_env = {c.get("env"): c.get("targets", []) for c in custom}
for s in j.get("secrets", []):
    if s.get("scope") == "global":
        for h in known.get(s["name"]) or by_env.get(s["name"]) or ["?"]:
            print(s["name"], h)
for c in custom:
    for h in c.get("targets", []):
        print(c.get("env"), h)')
echo "$global" | grep -q ' ?$' && die "a global sbx secret goes to hosts this script does not know: $(echo "$global" | awk '$2=="?"{print $1}')"
sbx mcp ls 2>/dev/null | grep -q 'No MCP servers registered' || die "MCP servers are registered with sbx; remove them first"

from=${HEATCTL_FROM_REPO:-}
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

# Two sandboxes, prepared the same way. The build sandbox holds the repository read-write and
# runs the build and fix sessions. The review sandbox, created before the first review, has an
# empty scratch directory as its workspace and the repository mounted read-only at the same
# path, so a reviewer cannot change the code it tests, as root either: the mount is read-only
# on the host's side.
review_sb=$run-review
cleanup() {  # removing what is already gone, or never made, is not an error
  for sb in "$run" "$review_sb"; do
    sbx rm -f "$sb" >/dev/null 2>&1 || true
    sbx secret rm --placeholder "heatctl-build-$sb" -f >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT
echo "$global" | sed '/^$/d' >"$dir/global-secrets.txt"

glob() {  # any case, and Go's module-path escape: each letter may follow anything
  python3 -c 'import sys; print("".join(f"*[{c.lower()}{c.upper()}]" if c.isalpha() else c for c in sys.argv[1]))' "$1"
}
allowed() {  # sandbox target
  sbx policy check network --sandbox "$1" --json "$2" \
    | python3 -c 'import sys,json; sys.exit(0 if json.load(sys.stdin)["allowed"] else 1)'
}
prepare() {  # label sandbox workspace...
  label=$1; sb=$2; shift 2
  # The build key never enters a sandbox: it sees a placeholder in ANTHROPIC_API_KEY, which the
  # sandbox proxy swaps for the key on requests to the model proxy. Scoped to the sandbox and
  # set before it is created, so the variable exists from the start.
  sbx secret set-custom --sandbox "$sb" --host "$model_host" --env ANTHROPIC_API_KEY \
    --placeholder "heatctl-build-$sb" --command "$key_command" >/dev/null 2>&1 \
    || die "the key command failed: $key_command"
  sbx create --name "$sb" --pull "${HEATCTL_PULL:-never}" claude "$@" >"$dir/$label-sbx-create.log" 2>&1 \
    || die "could not create the $label sandbox: $dir/$label-sbx-create.log"
  sbx policy allow network --sandbox "$sb" "$(paste -s -d, - <"$here/allowlist.txt"),$model_host:$model_port" >/dev/null

  # The agent kit allows hosts of its own on every sandbox it creates. Deny the ones that are
  # not on the allowlist, so the sandbox reaches the allowlist and nothing else.
  kit=$(sbx policy ls --source kit --json | python3 -c '
import sys, json
listed = {l.strip() for l in open(sys.argv[1]) if l.strip()}
hosts = {r.split(":")[0] for x in json.load(sys.stdin).get("rules", [])
         if x.get("applies_to") == "sandbox:" + sys.argv[2] and x.get("decision") == "allow"
         for r in x.get("resources", [])}
print(",".join(sorted(hosts - listed)))' "$here/allowlist.txt" "$sb")
  [ -z "$kit" ] || sbx policy deny network --sandbox "$sb" "$kit" >/dev/null 2>&1
  echo "$kit" | tr , '\n' >"$dir/$label-kit-denied.txt"

  # Never reachable: this repository and the project's site, wherever a build could fetch
  # them from (denylist.txt). Deny rules win over allow rules.
  grep -v '^#' "$here/denylist.txt" | while read -r host path; do
    [ -n "$host" ] || continue
    if [ -z "$path" ]; then sbx policy deny network --sandbox "$sb" "$host" >/dev/null 2>&1; continue; fi
    g=$(glob "$path"); git=$(glob .git)
    for p in "$g" "$g/**" "$g$git" "$g$git/**"; do
      sbx policy deny network --sandbox "$sb" "$host" --method ANY --path "$p" >/dev/null 2>&1
    done
  done

  # Fail closed: refuse to go on unless the sandbox reaches what it may install from, and not
  # the house's network, the kit's own hosts or anything on the denylist.
  for h in example.com 10.0.0.1 172.16.0.1 192.168.1.1 169.254.169.254 api.anthropic.com \
    "$model_host:22" $must_not_reach $(echo "$kit" | tr , ' ') $(echo "$global" | awk '{print $2}' | sort -u); do
    allowed "$sb" "$h" && die "$h is reachable from the $label sandbox"
  done
  allowed "$sb" "$model_host:$model_port" || die "the model proxy $model_host:$model_port is not reachable"
  # Path rules are only enforced on a live request, so the rest is checked from inside: every
  # denylist entry, also in other cases and escapes, must get 403, and the controls must not.
  controls="https://proxy.golang.org/github.com/eclipse/paho.golang/@v/list https://github.com/eclipse/paho.golang $model_url/health/liveliness"
  denied=$(grep -v '^#' "$here/denylist.txt" | python3 -c '
import sys
for line in sys.stdin:
    f = line.split()
    if not f: continue
    host, path = f[0].replace("*.", "www."), (f[1] if len(f) > 1 else "/")
    for p in {path, path + "/x", path.swapcase() + ".GIT", "".join("!" + c if c.isalpha() else c for c in path)}:
        print(f"https://{host}{p}")')
  sbx exec "$sb" sh -c 'for u; do echo "$(curl -s -o /dev/null -m 20 -w "%{http_code}" "$u") $u"; done' \
    sh $controls $denied >"$dir/$label-reach-check.txt" 2>"$dir/$label-reach-check.err"
  python3 -c '
import sys
codes = {u: c for c, u in (l.split() for l in open(sys.argv[1]) if l.strip())}
bad = [u for u in sys.argv[2:] if not codes.get(u, "000")[0] in "23"]
bad += [u for u, c in codes.items() if u not in sys.argv[2:] and c != "403"]
print("\n".join(bad), file=sys.stderr)
sys.exit(1 if bad else 0)' "$dir/$label-reach-check.txt" $controls || die "reach check failed: $dir/$label-reach-check.txt"

  # The build key reaches the model, and nothing it can do on the proxy goes beyond that.
  admin=$(sbx exec -e "U=$model_url" "$sb" sh -c 'curl -s -m 20 -o /dev/null -w "%{http_code}" -X POST "$U/key/generate" -H "Authorization: Bearer $ANTHROPIC_API_KEY" -H "content-type: application/json" -d "{}"' 2>/dev/null)
  case $admin in 401|403) ;; *) die "the build key can reach the proxy's admin routes from the $label sandbox (HTTP $admin)";; esac
  sbx policy log "$sb" --json >"$dir/$label-network-before.json"
}

t0=$(now)
prepare build "$run" "$repo"
sbx exec "$run" claude --version >"$dir/claude-version.txt" 2>&1
t1=$(now)

# Every session is a fresh Claude Code session, run from the repository's path. They share one
# time budget: the watchdog stops both sandboxes when the run's time is up, and no session
# starts after that.
( sleep "$timeout" && { sbx stop "$run"; sbx stop "$review_sb"; echo timeout >"$dir/timeout"; } ) >/dev/null 2>&1 &
watchdog=$!
session() {  # sandbox name prompt
  [ ! -f "$dir/timeout" ] || return 124
  echo "$2" >>"$dir/sessions.txt"
  sbx exec -w "$repo" -e DISABLE_AUTOUPDATER=1 -e DISABLE_TELEMETRY=1 -e ANTHROPIC_BASE_URL="$model_url" \
    -e ANTHROPIC_DEFAULT_HAIKU_MODEL="$small_model" \
    -e "ANTHROPIC_CUSTOM_HEADERS=x-litellm-tags: heatctl,harness=H0,run=$run,session=$2" "$1" \
    claude -p "$3" --model "$model" --output-format stream-json --verbose \
    --dangerously-skip-permissions --strict-mcp-config --disallowedTools WebSearch \
    </dev/null >"$dir/$2.jsonl" 2>"$dir/$2.stderr"
}
snapshot() {  # the working tree as a git tree, untracked files included, ignored ones not
  GIT_INDEX_FILE="$dir/.snapshot-index" git -C "$repo" add -A . >/dev/null 2>&1
  GIT_INDEX_FILE="$dir/.snapshot-index" git -C "$repo" write-tree
  rm -f "$dir/.snapshot-index"
}
status=0
[ -n "$from" ] || session "$run" build "$(cat "$here/prompt.txt")" || status=$?

# Review, and fix the high issues, until none is high. The repository is read-only to the
# reviewer; should it change anyway, that is recorded, the change kept as a patch, and undone.
tr0=$(now)
mkdir -p "$dir/review-scratch"
[ -f "$dir/timeout" ] || prepare review "$review_sb" "$dir/review-scratch" "$repo:ro"
tr1=$(now)
review=0; stop=
while [ -z "$stop" ]; do
  review=$((review + 1))
  head=$(git -C "$repo" rev-parse HEAD); before=$(snapshot)
  sbx exec "$review_sb" rm -f /tmp/heatctl-review.json >/dev/null 2>&1 || true
  session "$review_sb" "review-$review" "$(cat "$here/review-prompt.txt")" || true
  sbx exec "$review_sb" cat /tmp/heatctl-review.json >"$dir/review-$review.json" 2>/dev/null || true
  after=$(snapshot)
  if [ "$(git -C "$repo" rev-parse HEAD)" != "$head" ] || [ "$after" != "$before" ]; then
    echo "review-$review" >>"$dir/review-changed-repo.txt"
    { git -C "$repo" log --format='commit %h %s' "$head..HEAD"; git -C "$repo" diff "$before" "$after"; } \
      >"$dir/review-$review-changes.patch" 2>&1
    git -C "$repo" reset -q "$head"
    git -C "$repo" diff --name-only --diff-filter=A "$before" "$after" | (cd "$repo" && xargs rm -f --)
    git -C "$repo" checkout -q "$before" -- . && git -C "$repo" reset -q
  fi
  high=$(python3 -c '
import json, sys
try:
    issues = json.load(open(sys.argv[1]))["issues"]
    print(sum(1 for i in issues if str(i.get("severity", "")).lower() == "high"))
except Exception:
    print(-1)' "$dir/review-$review.json")
  if [ -f "$dir/timeout" ]; then stop="time is up"
  elif [ "$high" -lt 0 ]; then stop="review $review wrote no readable result"
  elif [ "$high" -eq 0 ]; then stop="no high issues"
  elif [ "$review" -gt 5 ]; then stop="5 fix rounds done"
  else
    session "$run" "fix-$review" "$(cat "$here/fix-prompt.txt"; echo; cat "$dir/review-$review.json")" || true
  fi
done
echo "$stop" >"$dir/stop.txt"
pkill -P "$watchdog" 2>/dev/null; kill "$watchdog" 2>/dev/null || true
t2=$(now)

for label in build review; do
  sb=$run; [ "$label" = build ] || sb=$review_sb
  sbx policy log "$sb" --json >"$dir/$label-network-after.json" 2>/dev/null || true
done
cleanup
trap - EXIT
t3=$(now)

python3 - "$dir" "$run" "$spec" "$model" "$status" "$t0" "$t1" "$t2" "$t3" "$small_model" "$tr0" "$tr1" <<'EOF'
import hashlib, json, os, re, subprocess, sys
d, run, spec, model, status, t0, t1, t2, t3, small_model, tr0, tr1 = sys.argv[1:]
t0, t1, t2, t3, tr0, tr1 = map(float, (t0, t1, t2, t3, tr0, tr1))
def sh(*a):
    return subprocess.run(a, capture_output=True, text=True).stdout.strip()
def result(name):
    for line in open(os.path.join(d, name + ".jsonl")):
        try:
            m = json.loads(line)
        except ValueError:
            continue
        if m.get("type") == "result":
            return {k: m.get(k) for k in ("subtype", "is_error", "num_turns", "duration_ms", "total_cost_usd", "usage")}
    return {}
names = open(os.path.join(d, "sessions.txt")).read().split() if os.path.exists(os.path.join(d, "sessions.txt")) else []
sessions = [dict(name=n, **result(n)) for n in names]
# Every clause and criterion id in the spec, to find what a review left on neither list.
spec_ids = set(re.findall(r"^- ((?:[A-Z][0-9]+|F[0-9]+\.AC[0-9]+))(?=:)", open(spec).read(), re.M))
reviews = []
for n in names:
    if n.startswith("review-"):
        try:
            issues = json.load(open(os.path.join(d, n + ".json")))["issues"]
            review = json.load(open(os.path.join(d, n + ".json")))
            # A clause named in an issue is not confirmed, whatever the reviewer's lists say.
            in_issues = set()
            for i in issues:
                in_issues |= set(re.findall(r"[A-Z][0-9]+(?:\.AC[0-9]+)?", str(i.get("clause", "")))) & spec_ids
            listed = set(review.get("confirmed", []))
            confirmed = listed - in_issues
            unconfirmed = set(review.get("unconfirmed", [])) | in_issues
            reviews.append({"name": n, **{s: sum(1 for i in issues if str(i.get("severity", "")).lower() == s) for s in ("high", "medium", "low")},
                            "not_tested": sum(1 for i in issues if str(i.get("status", "")).lower() in ("not tested", "not confirmed")),
                            "confirmed": len(confirmed & spec_ids), "unconfirmed": len(unconfirmed & spec_ids),
                            "confirmed_despite_an_issue": sorted(listed & in_issues),
                            "on_neither_list": sorted(spec_ids - confirmed - unconfirmed)})
        except Exception:
            reviews.append({"name": n, "unreadable": True})
def total(k):
    return round(sum(x.get(k) or 0 for x in sessions), 4)
result = {"num_turns": total("num_turns"), "total_cost_usd": total("total_cost_usd")}
changed = open(os.path.join(d, "review-changed-repo.txt")).read().split() if os.path.exists(os.path.join(d, "review-changed-repo.txt")) else []
def hosts(name):
    try:
        log = json.load(open(os.path.join(d, name)))
    except (OSError, ValueError):
        log = {}
    return {k: {e["host"]: e.get("count", 0) for e in log.get(k, [])} for k in ("allowed", "blocked")}
network = {"allowed": {}, "blocked": {}}
for label in ("build", "review"):
    before, after = hosts(label + "-network-before.json"), hosts(label + "-network-after.json")
    for k in after:
        for h, n in after[k].items():
            if n > before[k].get(h, 0):
                network[k][h] = network[k].get(h, 0) + n - before[k].get(h, 0)
specbytes = open(spec, "rb").read()
version = next((l.split(":", 1)[1].strip() for l in specbytes.decode().splitlines() if l.startswith("Spec version:")), None)
repo = os.path.join(d, "repo")
meta = {
    "run_id": run, "harness": "H0", "model": model, "small_model": small_model,
    "from_repo": open(os.path.join(d, "from.txt")).read().strip() if os.path.exists(os.path.join(d, "from.txt")) else None,
    "spec": {"version": version, "sha256": hashlib.sha256(specbytes).hexdigest()},
    "claude_code": open(os.path.join(d, "claude-version.txt")).read().strip(),
    "sbx": sh("sbx", "version").splitlines()[0] if sh("sbx", "version") else None,
    "template": [l for l in sh("sbx", "template", "ls").splitlines() if "claude-code" in l],
    "exit_status": int(status), "timed_out": os.path.exists(os.path.join(d, "timeout")),
    "seconds": {"sessions": round(t2 - t1 - (tr1 - tr0), 1), "sandbox_overhead": round((t1 - t0) + (tr1 - tr0) + (t3 - t2), 1)},
    "result": result,
    "sessions": sessions,
    "reviews": reviews,
    "stop": open(os.path.join(d, "stop.txt")).read().strip() if os.path.exists(os.path.join(d, "stop.txt")) else None,
    "reviews_that_changed_the_repo": changed,
    "network": network,
    "commits": int(sh("git", "-C", repo, "rev-list", "--count", "HEAD") or 0),
    "uncommitted": sh("git", "-C", repo, "status", "--porcelain").splitlines(),
}
json.dump(meta, open(os.path.join(d, "meta.json"), "w"), indent=1)
print(json.dumps({k: meta[k] for k in ("run_id", "exit_status", "timed_out", "seconds", "stop")}))
print("reviews:", json.dumps(reviews))
print("cost:", result.get("total_cost_usd"), "turns:", result.get("num_turns"), "archive:", d)
EOF
