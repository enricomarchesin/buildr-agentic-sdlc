# The cage, sourced by build.sh. A copy of H0's, with one difference: H1's loop runs on the host
# and holds the build key, so a sandbox has no key and no route to the model proxy. Where H0
# allowed the proxy and checked that the key reached nothing else on it, H1 checks that the
# proxy is refused. Every other rule is H0's.
#
# The cage, sourced by build.sh: the checks on this host before a run, the two sandboxes'
# network rules and the checks that they hold, and their removal. Uses here, dir, run,
# review_sb, model_url, model_host, model_port, key_command and must_not_reach from build.sh.

# The only credential a build may use is the build key. A secret shared with every sandbox is
# tolerated only if every host it is sent to is refused from the build's sandbox, which is
# checked once the sandbox exists; a global secret whose hosts are not known stops the run.
# The kit's MCP gateway is reachable from every sandbox and no policy closes it; it serves only
# the MCP servers registered on this host, so none may be. Sets global: one "name host" a line.
preflight() {
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
  mcp=$(sbx mcp ls 2>&1) || true  # exits 0 on errors too, such as being signed out of Docker, and may print progress lines before the error
  case $mcp in
    *'No MCP servers registered'*) ;;
    *) err=$(echo "$mcp" | sed -n 's/^[Ee]rror: //p' | head -1)
       [ -z "$err" ] || die "sbx mcp ls: $err"
       die "MCP servers are registered with sbx; remove them first" ;;
  esac
}

cleanup() {  # removing what is already gone, or never made, is not an error
  for sb in "$run" "$review_sb"; do
    sbx rm -f "$sb" >/dev/null 2>&1 || true
    sbx secret rm --placeholder "heatctl-build-$sb" -f >/dev/null 2>&1 || true
  done
}

glob() {  # any case, and Go's module-path escape: each letter may follow anything
  python3 -c 'import sys; print("".join(f"*[{c.lower()}{c.upper()}]" if c.isalpha() else c for c in sys.argv[1]))' "$1"
}
allowed() {  # sandbox target
  sbx policy check network --sandbox "$1" --json "$2" \
    | python3 -c 'import sys,json; sys.exit(0 if json.load(sys.stdin)["allowed"] else 1)'
}
kit_hosts() {  # sandbox: the hosts the agent kit allows on it that are not on the allowlist, comma-separated
  sbx policy ls --source kit --json | python3 -c '
import sys, json
listed = {l.strip() for l in open(sys.argv[1]) if l.strip()}
hosts = {r.split(":")[0] for x in json.load(sys.stdin).get("rules", [])
         if x.get("applies_to") == "sandbox:" + sys.argv[2] and x.get("decision") == "allow"
         for r in x.get("resources", [])}
print(",".join(sorted(hosts - listed)))' "$here/allowlist.txt" "$1"
}
denied_urls() {  # every denylist entry as URLs, also in other cases and escapes
  grep -v '^#' "$here/denylist.txt" | python3 -c '
import sys
for line in sys.stdin:
    f = line.split()
    if not f: continue
    host, path = f[0].replace("*.", "www."), (f[1] if len(f) > 1 else "/")
    for p in {path, path + "/x", path.swapcase() + ".GIT", "".join("!" + c if c.isalpha() else c for c in path)}:
        print(f"https://{host}{p}")'
}
reach_ok() {  # file control...: every control answered 2xx or 3xx, everything else 403
  python3 -c '
import sys
codes = {u: c for c, u in (l.split() for l in open(sys.argv[1]) if l.strip())}
bad = [u for u in sys.argv[2:] if not codes.get(u, "000")[0] in "23"]
bad += [u for u, c in codes.items() if u not in sys.argv[2:] and c != "403"]
print("\n".join(bad), file=sys.stderr)
sys.exit(1 if bad else 0)' "$@"
}

# Two sandboxes, prepared the same way. The build sandbox holds the repository read-write and
# runs the build and fix sessions. The review sandbox, created before the first review, has an
# empty scratch directory as its workspace and the repository mounted read-only at the same
# path, so a reviewer cannot change the code it tests, as root either: the mount is read-only
# on the host's side.
prepare() {  # label sandbox workspace...
  label=$1; sb=$2; shift 2
  # The build key stays on the host, with the loop; no sandbox has it.
  sbx create --name "$sb" --pull "${HEATCTL_PULL:-never}" claude "$@" >"$dir/$label-sbx-create.log" 2>&1 \
    || die "could not create the $label sandbox: $dir/$label-sbx-create.log"
  sbx policy allow network --sandbox "$sb" "$(paste -s -d, - <"$here/allowlist.txt")" >/dev/null

  # The agent kit allows hosts of its own on every sandbox it creates. Deny the ones that are
  # not on the allowlist, so the sandbox reaches the allowlist and nothing else.
  kit=$(kit_hosts "$sb")
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
    "$model_host:$model_port" "$model_host:22" $must_not_reach $(echo "$kit" | tr , ' ') $(echo "$global" | awk '{print $2}' | sort -u); do
    allowed "$sb" "$h" && die "$h is reachable from the $label sandbox"
  done
  # Path rules are only enforced on a live request, so the rest is checked from inside: every
  # denylist entry must get 403, and the controls must not.
  controls="https://proxy.golang.org/github.com/eclipse/paho.golang/@v/list https://github.com/eclipse/paho.golang"
  # A control that fails is asked again a few times before it counts: the upstream can answer 504 to
  # a request that nothing in this cage had a say in. The denylist is asked once and must get 403.
  sbx exec "$sb" sh -c 'for u; do for try in 1 2 3 4 5 6; do c=$(curl -s -o /dev/null -m 20 -w "%{http_code}" "$u"); case $c in 2*|3*) break;; esac; sleep 10; done; echo "$c $u"; done' \
    sh $controls >"$dir/$label-reach-check.txt" 2>"$dir/$label-reach-check.err"
  sbx exec "$sb" sh -c 'for u; do echo "$(curl -s -o /dev/null -m 20 -w "%{http_code}" "$u") $u"; done' \
    sh $(denied_urls) >>"$dir/$label-reach-check.txt" 2>>"$dir/$label-reach-check.err"
  reach_ok "$dir/$label-reach-check.txt" $controls || die "reach check failed: $dir/$label-reach-check.txt"

  sbx policy log "$sb" --json >"$dir/$label-network-before.json"
}

network_logs() {  # each sandbox's network log at the end, to set against the one taken before
  for label in build review; do
    sb=$run; [ "$label" = build ] || sb=$review_sb
    sbx policy log "$sb" --json >"$dir/$label-network-after.json" 2>/dev/null || true
  done
}
