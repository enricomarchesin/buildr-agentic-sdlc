#!/bin/sh
# H0: one build. The whole spec and an empty repository go into a fresh Docker sandbox, Claude
# Code builds until it declares done, and everything the wrapper can see is archived. The
# sandbox may install from anywhere on allowlist.txt, and never reaches denylist.txt.
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

# The only credential a build may see is the model key: no secret may be global. The kit's MCP
# gateway is reachable from every sandbox and no policy closes it; it serves only the MCP
# servers registered on this host, so none may be.
sbx secret ls 2>/dev/null | grep -q '(global)' && die "global sbx secrets exist; scope them to their sandboxes first"
sbx mcp ls 2>/dev/null | grep -q 'No MCP servers registered' || die "MCP servers are registered with sbx; remove them first"

mkdir -p "$repo"
cp "$spec" "$repo/SPEC.md"
git -C "$repo" init -q
git -C "$repo" -c user.name=harness -c user.email=harness@localhost add SPEC.md
git -C "$repo" -c user.name=harness -c user.email=harness@localhost commit -qm "spec"

t0=$(now)
# The kit wires the key into Claude Code only if it exists when the sandbox is created.
trap 'sbx rm -f "$run" >/dev/null 2>&1; sbx secret rm anthropic --sandbox "$run" -f >/dev/null 2>&1 || true' EXIT
sbx secret set anthropic --sandbox "$run" --command "$key_command" >/dev/null 2>&1 \
  || die "the key command failed: $key_command"
sbx create --name "$run" --pull "${HEATCTL_PULL:-never}" claude "$repo" >"$dir/sbx-create.log" 2>&1
sbx policy allow network --sandbox "$run" "$(paste -s -d, - <"$here/allowlist.txt")" >/dev/null

# The agent kit allows hosts of its own on every sandbox it creates. Deny the ones that are
# not on the allowlist, so the build reaches the allowlist and nothing else.
kit=$(sbx policy ls --source kit --json | python3 -c '
import sys, json
listed = {l.strip() for l in open(sys.argv[1]) if l.strip()}
hosts = {r.split(":")[0] for x in json.load(sys.stdin).get("rules", [])
         if x.get("applies_to") == "sandbox:" + sys.argv[2] and x.get("decision") == "allow"
         for r in x.get("resources", [])}
print(",".join(sorted(hosts - listed)))' "$here/allowlist.txt" "$run")
[ -z "$kit" ] || sbx policy deny network --sandbox "$run" "$kit" >/dev/null 2>&1
echo "$kit" | tr , '\n' >"$dir/kit-denied.txt"

# Never reachable: this repository and the project's site, wherever a build could fetch
# them from (denylist.txt). Deny rules win over allow rules.
glob() {  # any case, and Go's module-path escape: each letter may follow anything
  python3 -c 'import sys; print("".join(f"*[{c.lower()}{c.upper()}]" if c.isalpha() else c for c in sys.argv[1]))' "$1"
}
grep -v '^#' "$here/denylist.txt" | while read -r host path; do
  [ -n "$host" ] || continue
  if [ -z "$path" ]; then sbx policy deny network --sandbox "$run" "$host" >/dev/null 2>&1; continue; fi
  g=$(glob "$path"); git=$(glob .git)
  for p in "$g" "$g/**" "$g$git" "$g$git/**"; do
    sbx policy deny network --sandbox "$run" "$host" --method ANY --path "$p" >/dev/null 2>&1
  done
done

# Fail closed: refuse to build unless the sandbox reaches what it may install from, and not
# the house's network, the kit's own hosts or anything on the denylist.
allowed() {
  sbx policy check network --sandbox "$run" --json "$1" \
    | python3 -c 'import sys,json; sys.exit(0 if json.load(sys.stdin)["allowed"] else 1)'
}
for h in example.com 10.0.0.1 172.16.0.1 192.168.1.1 169.254.169.254 $(echo "$kit" | tr , ' '); do
  allowed "$h" && die "$h is reachable"
done
# Path rules are only enforced on a live request, so the rest is checked from inside: every
# denylist entry, also in other cases and escapes, must get 403, and the two controls must not.
controls="https://proxy.golang.org/github.com/eclipse/paho.golang/@v/list https://github.com/eclipse/paho.golang"
denied=$(grep -v '^#' "$here/denylist.txt" | python3 -c '
import sys
for line in sys.stdin:
    f = line.split()
    if not f: continue
    host, path = f[0].replace("*.", "www."), (f[1] if len(f) > 1 else "/")
    for p in {path, path + "/x", path.swapcase() + ".GIT", "".join("!" + c if c.isalpha() else c for c in path)}:
        print(f"https://{host}{p}")')
sbx exec "$run" sh -c 'for u; do echo "$(curl -s -o /dev/null -m 20 -w "%{http_code}" "$u") $u"; done' \
  sh $controls $denied >"$dir/reach-check.txt" 2>"$dir/reach-check.err"
python3 - "$dir/reach-check.txt" $controls <<'EOF' || die "reach check failed: $dir/reach-check.txt"
import sys
codes = {u: c for c, u in (l.split() for l in open(sys.argv[1]) if l.strip())}
bad = [u for u in sys.argv[2:] if not codes.get(u, "000")[0] in "23"]
bad += [u for u, c in codes.items() if u not in sys.argv[2:] and c != "403"]
print("\n".join(bad), file=sys.stderr)
sys.exit(1 if bad else 0)
EOF

sbx exec "$run" claude --version >"$dir/claude-version.txt" 2>&1
sbx policy log "$run" --json >"$dir/network-before.json"
t1=$(now)

sbx exec -e DISABLE_AUTOUPDATER=1 -e DISABLE_TELEMETRY=1 "$run" \
  claude -p "$(cat "$here/prompt.txt")" --model "$model" --output-format stream-json --verbose \
  --dangerously-skip-permissions --strict-mcp-config --disallowedTools WebSearch >"$dir/claude.jsonl" 2>"$dir/claude.stderr" &
pid=$!
( sleep "$timeout" && sbx stop "$run" && echo timeout >"$dir/timeout" ) >/dev/null 2>&1 &
watchdog=$!
status=0; wait "$pid" || status=$?
pkill -P "$watchdog" 2>/dev/null; kill "$watchdog" 2>/dev/null || true
t2=$(now)

sbx policy log "$run" --json >"$dir/network-after.json"
sbx rm -f "$run" >/dev/null
sbx secret rm anthropic --sandbox "$run" -f >/dev/null 2>&1 || true
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
