"""The record of a run, for build.sh.

    meta.py high REVIEW_JSON                           the review's high issues, -1 if unreadable
    meta.py write DIR RUN SPEC MODEL SMALL_MODEL STATUS    writes DIR/meta.json, prints a summary

The times come from DIR/times.txt, one "step seconds" a line, written by build.sh as it goes.
"""
import hashlib, json, os, re, subprocess, sys

HERE = os.path.dirname(os.path.abspath(__file__))


def sh(*a):
    return subprocess.run(a, capture_output=True, text=True).stdout.strip()


def high(path):
    try:
        issues = json.load(open(path))["issues"]
        return sum(1 for i in issues if str(i.get("severity", "")).lower() == "high")
    except Exception:
        return -1


def write(d, run, spec, model, small_model, status):
    def path(name):
        return os.path.join(d, name)
    def text(name):
        return open(path(name)).read().strip() if os.path.exists(path(name)) else None
    def result(name):
        for line in open(path(name + ".jsonl")):
            try:
                m = json.loads(line)
            except ValueError:
                continue
            if m.get("type") == "result":
                return {k: m.get(k) for k in ("subtype", "is_error", "num_turns", "duration_ms", "total_cost_usd", "usage")}
        return {}
    names = (text("sessions.txt") or "").split()
    sessions = [dict(name=n, **result(n)) for n in names]

    # Every clause and criterion id in the spec, to find what a review left on neither list.
    spec_ids = set(re.findall(r"^- ((?:[A-Z][0-9]+|F[0-9]+\.AC[0-9]+))(?=:)", open(spec).read(), re.M))
    reviews = []
    for n in names:
        if n.startswith("review-"):
            try:
                review = json.load(open(path(n + ".json")))
                issues = review["issues"]
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
    totals = {"num_turns": total("num_turns"), "total_cost_usd": total("total_cost_usd")}

    def hosts(name):
        try:
            log = json.load(open(path(name)))
        except (OSError, ValueError):
            log = {}
        return {k: {e["host"]: e.get("count", 0) for e in log.get(k, [])} for k in ("allowed", "blocked")}
    network = {"allowed": {}, "blocked": {}}
    for label in ("build", "review"):
        before, after = hosts(label + "-network-before.json"), hosts(label + "-network-after.json")
        for k in after:
            for h, c in after[k].items():
                if c > before[k].get(h, 0):
                    network[k][h] = network[k].get(h, 0) + c - before[k].get(h, 0)

    # Setting up and removing the sandboxes is overhead; the rest of the run is sessions.
    t = {k: float(v) for k, v in (l.split() for l in open(path("times.txt")) if l.strip())}
    review_setup = t["review-ready"] - t["review-prep"]
    seconds = {"sessions": round(t["loop-done"] - t["build-ready"] - review_setup, 1),
               "sandbox_overhead": round((t["build-ready"] - t["start"]) + review_setup + (t["end"] - t["loop-done"]), 1)}

    specbytes = open(spec, "rb").read()
    version = next((l.split(":", 1)[1].strip() for l in specbytes.decode().splitlines() if l.startswith("Spec version:")), None)
    repo = path("repo")
    sbx_version = sh("sbx", "version")
    meta = {
        "run_id": run, "harness": "H0", "model": model, "small_model": small_model,
        # The harness that ran, and whether its files differed from that commit.
        "harness_commit": sh("git", "-C", HERE, "rev-parse", "HEAD") or None,
        "harness_modified": bool(sh("git", "-C", HERE, "status", "--porcelain", "--", ".")),
        "from_repo": text("from.txt"),
        "spec": {"version": version, "sha256": hashlib.sha256(specbytes).hexdigest()},
        "claude_code": text("claude-version.txt"),
        "sbx": sbx_version.splitlines()[0] if sbx_version else None,
        "template": [l for l in sh("sbx", "template", "ls").splitlines() if "claude-code" in l],
        "exit_status": int(status), "timed_out": os.path.exists(path("timeout")),
        "seconds": seconds,
        "result": totals,
        "sessions": sessions,
        "reviews": reviews,
        "stop": text("stop.txt"),
        "reviews_that_changed_the_repo": (text("review-changed-repo.txt") or "").split(),
        "network": network,
        "commits": int(sh("git", "-C", repo, "rev-list", "--count", "HEAD") or 0),
        "uncommitted": sh("git", "-C", repo, "status", "--porcelain").splitlines(),
    }
    json.dump(meta, open(path("meta.json"), "w"), indent=1)
    print(json.dumps({k: meta[k] for k in ("run_id", "exit_status", "timed_out", "seconds", "stop")}))
    print("reviews:", json.dumps(reviews))
    print("cost:", totals.get("total_cost_usd"), "turns:", totals.get("num_turns"), "archive:", d)


if __name__ == "__main__":
    if sys.argv[1:2] == ["high"]:
        print(high(sys.argv[2]))
    elif sys.argv[1:2] == ["write"]:
        write(*sys.argv[2:8])
    else:
        sys.exit(__doc__)
