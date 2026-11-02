#!/bin/sh
# H0: one build. The whole spec and an empty repository go into a fresh Docker sandbox, Claude
# Code builds until it declares done, and everything the wrapper can see is archived.
#
#   harness/run/build.sh [SPEC]        default: spec/SPEC.md in this repository
#
#   HEATCTL_BUILDS       archive root, default ~/heatctl-builds
#   HEATCTL_MODEL        default claude-sonnet-5-5
#   HEATCTL_TIMEOUT      seconds before the build is stopped, default 14400
#   HEATCTL_KEY_COMMAND  host command printing the spend-limited model API key; default reads
#                        the macOS Keychain item heatctl-h0-anthropic
#   HEATCTL_PULL         sandbox image pull policy, default never: every build of a version
#                        runs on the same image. Use missing for the very first build.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
spec=${1:-$here/../../spec/SPEC.md}
model=${HEATCTL_MODEL:-claude-sonnet-5-5}
timeout=${HEATCTL_TIMEOUT:-14400}
key_command=${HEATCTL_KEY_COMMAND:-/usr/bin/security find-generic-password -a $USER -s heatctl-h0-anthropic -w}
export SBX_NO_TELEMETRY=1

run=h0-$(date -u +%Y%m%dT%H%M%SZ)-$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')
dir=${HEATCTL_BUILDS:-$HOME/heatctl-builds}/$run
repo=$dir/repo
now() { python3 -c 'import time; print(f"{time.time():.3f}")'; }
die() { echo "build.sh: $*" >&2; exit 1; }

# The only credential a build may see is the model key: no secret may be global.
sbx secret ls 2>/dev/null | grep -q '(global)' && die "global sbx secrets exist; scope them to their sandboxes first"

mkdir -p "$repo"
cp "$spec" "$repo/SPEC.md"
git -C "$repo" init -q
git -C "$repo" -c user.name=harness -c user.email=harness@localhost add SPEC.md
git -C "$repo" -c user.name=harness -c user.email=harness@localhost commit -qm "spec"

t0=$(now)
sbx create --name "$run" --pull "${HEATCTL_PULL:-never}" claude "$repo" >"$dir/sbx-create.log" 2>&1
trap 'sbx rm -f "$run" >/dev/null 2>&1 || true' EXIT
sbx policy allow network --sandbox "$run" "$(paste -s -d, - <"$here/allowlist.txt")" >/dev/null
sbx secret set anthropic --sandbox "$run" --command "$key_command" >/dev/null

# Fail closed: refuse to build unless the sandbox reaches the registries and nothing else.
allowed() {
  sbx policy check network --sandbox "$run" --json "$1" \
    | python3 -c 'import sys,json; sys.exit(0 if json.load(sys.stdin)["allowed"] else 1)'
}
for h in github.com example.com; do allowed "$h" && die "$h is reachable"; done
allowed proxy.golang.org || die "proxy.golang.org is not reachable"

sbx exec "$run" claude --version >"$dir/claude-version.txt" 2>&1
sbx policy log "$run" --json >"$dir/network-before.json"
t1=$(now)

sbx exec -e DISABLE_AUTOUPDATER=1 -e DISABLE_TELEMETRY=1 "$run" \
  claude -p "$(cat "$here/prompt.txt")" --model "$model" --output-format stream-json --verbose \
  --dangerously-skip-permissions >"$dir/claude.jsonl" 2>"$dir/claude.stderr" &
pid=$!
( sleep "$timeout" && sbx stop "$run" >/dev/null 2>&1 && echo timeout >"$dir/timeout" ) &
watchdog=$!
status=0; wait "$pid" || status=$?
kill "$watchdog" 2>/dev/null || true
t2=$(now)

sbx policy log "$run" --json >"$dir/network-after.json"
sbx rm -f "$run" >/dev/null
trap - EXIT
t3=$(now)

python3 - "$dir" "$run" "$spec" "$model" "$status" "$t0" "$t1" "$t2" "$t3" <<'EOF'
import hashlib, json, os, subprocess, sys
d, run, spec, model, status, t0, t1, t2, t3 = sys.argv[1:]
t0, t1, t2, t3 = map(float, (t0, t1, t2, t3))
def sh(*a):
    return subprocess.run(a, capture_output=True, text=True).stdout.strip()
result = {}
for line in open(os.path.join(d, "claude.jsonl")):
    try:
        m = json.loads(line)
    except ValueError:
        continue
    if m.get("type") == "result":
        result = {k: m.get(k) for k in ("subtype", "is_error", "num_turns", "duration_ms", "total_cost_usd", "usage")}
def hosts(name):
    log = json.load(open(os.path.join(d, name)))
    return {k: {e["host"]: e.get("count", 0) for e in log.get(k, [])} for k in ("allowed", "blocked")}
before, after = hosts("network-before.json"), hosts("network-after.json")
network = {k: {h: n - before[k].get(h, 0) for h, n in after[k].items() if n > before[k].get(h, 0)} for k in after}
specbytes = open(spec, "rb").read()
version = next((l.split(":", 1)[1].strip() for l in specbytes.decode().splitlines() if l.startswith("Spec version:")), None)
repo = os.path.join(d, "repo")
meta = {
    "run_id": run, "harness": "H0", "model": model,
    "spec": {"version": version, "sha256": hashlib.sha256(specbytes).hexdigest()},
    "claude_code": open(os.path.join(d, "claude-version.txt")).read().strip(),
    "sbx": sh("sbx", "version").splitlines()[0] if sh("sbx", "version") else None,
    "template": [l for l in sh("sbx", "template", "ls").splitlines() if "claude-code" in l],
    "exit_status": int(status), "timed_out": os.path.exists(os.path.join(d, "timeout")),
    "seconds": {"build": round(t2 - t1, 1), "sandbox_overhead": round((t1 - t0) + (t3 - t2), 1)},
    "result": result,
    "network": network,
    "commits": int(sh("git", "-C", repo, "rev-list", "--count", "HEAD") or 0),
    "uncommitted": sh("git", "-C", repo, "status", "--porcelain").splitlines(),
}
json.dump(meta, open(os.path.join(d, "meta.json"), "w"), indent=1)
print(json.dumps({k: meta[k] for k in ("run_id", "exit_status", "timed_out", "seconds")}))
print("cost:", result.get("total_cost_usd"), "turns:", result.get("num_turns"), "archive:", d)
EOF
