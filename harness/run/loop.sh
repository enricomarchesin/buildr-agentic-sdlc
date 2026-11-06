# The sessions, sourced by build.sh: how one agent session runs, and the review and fix
# rounds. Uses here, dir, repo, run, review_sb, model, small_model, model_url and timeout
# from build.sh.

# Every session is a fresh Claude Code session, run from the repository's path. They share one
# time budget: the watchdog stops both sandboxes when the run's time is up, and no session
# starts after that.
watchdog_start() {
  ( sleep "$timeout" && { sbx stop "$run"; sbx stop "$review_sb"; echo timeout >"$dir/timeout"; } ) >/dev/null 2>&1 &
  watchdog=$!
}
watchdog_stop() {
  pkill -P "$watchdog" 2>/dev/null; kill "$watchdog" 2>/dev/null || true
}
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

# The repository is read-only to the reviewer; should it change anyway, that is recorded, the
# change kept as a patch, and undone.
review() {  # n
  head=$(git -C "$repo" rev-parse HEAD); before=$(snapshot)
  sbx exec "$review_sb" rm -f /tmp/heatctl-review.json >/dev/null 2>&1 || true
  session "$review_sb" "review-$1" "$(cat "$here/review-prompt.txt")" || true
  sbx exec "$review_sb" cat /tmp/heatctl-review.json >"$dir/review-$1.json" 2>/dev/null || true
  after=$(snapshot)
  if [ "$(git -C "$repo" rev-parse HEAD)" != "$head" ] || [ "$after" != "$before" ]; then
    echo "review-$1" >>"$dir/review-changed-repo.txt"
    { git -C "$repo" log --format='commit %h %s' "$head..HEAD"; git -C "$repo" diff "$before" "$after"; } \
      >"$dir/review-$1-changes.patch" 2>&1
    git -C "$repo" reset -q "$head"
    git -C "$repo" diff --name-only --diff-filter=A "$before" "$after" | (cd "$repo" && xargs rm -f --)
    git -C "$repo" checkout -q "$before" -- . && git -C "$repo" reset -q
  fi
}

# Review, and fix the high issues, until none is high. Every review is a fresh session that
# sees no earlier review; a fix session sees the review before it.
rounds() {
  n=0; stop=
  while [ -z "$stop" ]; do
    n=$((n + 1))
    review "$n"
    high=$(python3 "$here/meta.py" high "$dir/review-$n.json")
    if [ -f "$dir/timeout" ]; then stop="time is up"
    elif [ "$high" -lt 0 ]; then stop="review $n wrote no readable result"
    elif [ "$high" -eq 0 ]; then stop="no high issues"
    elif [ "$n" -gt 5 ]; then stop="5 fix rounds done"
    else
      session "$run" "fix-$n" "$(cat "$here/fix-prompt.txt"; echo; cat "$dir/review-$n.json")" || true
    fi
  done
  echo "$stop" >"$dir/stop.txt"
}
