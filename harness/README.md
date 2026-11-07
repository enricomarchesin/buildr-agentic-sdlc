# harness/

The harness is the product of the series. It takes the whole spec from `spec/` and an empty
repo, gets a plan from the model, builds the app, runs the gates, and commits on green. Each
run is one build.

Current version: H0 (see `CHANGELOG.md`).

## What it is not

- Not a framework comparison. Frameworks get measured inside an ADR, not adopted as versions.
- Not a benchmark. There is no cross-model score, ever.
- Not a product of its own. If a version does not answer a failure that has already been shown,
  it does not happen.

## Structure

```
harness/
  README.md
  CHANGELOG.md          one row per version
  adr/                  000-template.md, 001-..., one per version
  run-h0/               H0, the baseline: a wrapper around a vendor coding harness
                        in a Docker Sandbox: build.sh, the run; cage.sh, the sandboxes and their
                        network rules; loop.sh, the sessions and the review and fix rounds;
                        meta.py, the record; the three prompts, allowlist.txt, denylist.txt
  run-h1/               H1: the same run with the loop ours, on a Python agent framework; see its README
```

H0 has no run records. The vendor harness streams each session as messages, tool calls and
results, and the build archives that stream (`build.jsonl`, `review-N.jsonl`), but nothing in
it is tied to a commit, a check or a decision. Directories are added here when a version needs
them.

## Version discipline

- One row per version in `CHANGELOG.md`.
- One axis per version: `control` or `substrate`. H0 is the `baseline`. A compound change is labelled and the ADR
  states what cannot be attributed.
- Every version has an ADR with: context, symptom, alternatives rejected, decision,
  consequences, *if your product differs*.
- LOC owned is measured, not estimated. Count the harness code you maintain, excluding the
  fixture and the evals.
- A version is not a release note. It exists when the numbers before and after are filled in.

## Running it

```
harness/run-h0/build.sh [SPEC]
```

One run is one build. `build.sh` creates a fresh Docker Sandbox (`sbx`, one microVM per
sandbox) with the claude agent and an empty repository holding only `SPEC.md`, gives it the
network rules below and the build key, runs Claude Code headless with `prompt.txt` until it
declares done, runs the review and fix rounds described under Gates, then archives the
repository, every session's output stream, every review, the sandbox's network log and a
`meta.json` with cost, turns and times per session and in total, and removes the sandbox.
`HEATCTL_*` variables at the top of the script set the model proxy, the model, the timeout,
the archive and where the key comes from. Those that describe the operator's own network,
such as the proxy's address, can live in a file outside the repository,
`~/.config/heatctl/build.env`. `HEATCTL_CHECK_ONLY=1` prepares both sandboxes, runs the network checks
below and stops before any session, so a change to the network rules can be tried at no
cost.

**The model proxy.** Claude Code does not call a model vendor directly. It talks, in
Anthropic's format, to a LiteLLM proxy on the operator's network (`HEATCTL_MODEL_URL`),
which forwards to whichever provider serves the model. Every version uses the same proxy and
the same build key, so a version that changes the model changes only its name. The proxy
logs every call with its cost, tagged with the run id, which gives each build's model
spend.

The sandbox mounts the repository read-write and nothing else from the host. The agent has
no route to the sandbox daemon, so it cannot change its own policy. H0 does not claim this
is the right sandbox; the ADR records that it was adopted as a safe default, to be justified
or replaced once the runs show what the agent touches.

### Network

The rule: **a build may fetch anything it installs, reads nothing about this project, and
never reaches the house.** Inside its microVM the agent may install what it likes, from
language registries, OS packages, container images or release downloads. What it must not
do is read this repository or the project's site, which describe how builds are graded and
what earlier builds got wrong: that would credit a version with what it read. And it must
never reach the house's network, where the real relays are.

The sandbox has no route out except the proxy. The host's global policy is deny-all; for
each build's sandbox alone, `cage.sh` allows the hosts in `run-h0/allowlist.txt` and denies
those in `run-h0/denylist.txt`. A deny wins over an allow.

| Allowed | Hosts |
|---|---|
| Go | `proxy.golang.org`, `sum.golang.org`, `storage.googleapis.com` (toolchain downloads) |
| Python | `pypi.org`, `files.pythonhosted.org` |
| Node | `registry.npmjs.org`, `registry.yarnpkg.com` |
| Rust | `index.crates.io`, `static.crates.io`, `crates.io`, `static.rust-lang.org`, `sh.rustup.rs` |
| Java and Kotlin | `repo.maven.apache.org`, `repo1.maven.org`, `plugins.gradle.org`, `services.gradle.org` |
| .NET | `api.nuget.org` |
| Ruby | `rubygems.org`, `index.rubygems.org` |
| PHP | `repo.packagist.org` |
| GitHub | `github.com`, `api.github.com`, `codeload.github.com`, `raw.githubusercontent.com`, `objects.githubusercontent.com`, `release-assets.githubusercontent.com` |
| Container images | `registry-1.docker.io`, `auth.docker.io`, `production.cloudflare.docker.com`, `production.cloudfront.docker.com`, `ghcr.io`, `pkg-containers.githubusercontent.com` |
| OS packages | the image's apt sources: `ports.ubuntu.com`, `download.docker.com`, `cli.github.com` |
| Model proxy | `HEATCTL_MODEL_URL`, by exact host and port: the one private address a build reaches. No model vendor's own API is reachable |

| Denied | How |
|---|---|
| This repository | by path, on every host above that serves it: GitHub's web, API, archive and raw hosts, and the Go module proxy, which serves any public GitHub repository as a module. Matched without regard to case, with or without `.git`, and in Go's escaped form |
| The project's site | the whole domain |
| The agent kit's own hosts | every host the kit allows on its own that is not on the allowlist: the vendor's API, console, docs, updates, connectors and remote control |
| Everything else | the general web, search engines, and every private address range, the house's included |

Rules that go with it:

- **No web search.** Claude Code's web search runs on the vendor's side, out of the proxy's
  reach, so it is turned off (`--disallowedTools WebSearch`). Its fetch tool goes through
  the proxy and stays.
- **No MCP servers.** The kit connects every sandbox to an MCP gateway that the policy
  cannot close. Claude Code starts with none (`--strict-mcp-config`), and the run refuses to
  start if any MCP server is registered on the host, since the gateway would offer it.
- **One credential, outside the sandbox.** The build key for the model proxy is the only
  secret a build can use, and it never enters the sandbox: the sandbox holds a placeholder,
  which the sandbox proxy swaps for the key on requests to the model proxy. The key is used
  for builds only, has a budget generous enough never to stop a normal build, and cannot
  reach the proxy's admin routes. No other credentials, no SSH keys, no registry tokens, so
  nothing can be published. A secret the operator shares with every sandbox is tolerated
  only if each host it is sent to is refused from the build's sandbox, checked before every
  build; a shared secret whose hosts the script does not know stops the run.
- **Fail closed.** Before the build starts, the run checks that the controls and the model
  proxy are reachable; that `example.com`, the private ranges, the kit's hosts, the proxy
  host's other ports, the addresses the operator lists as never to be reached (the house's
  broker among them) and every denied path, in other cases and escapes, are refused; and
  that the build key gets no further than the model. If any check fails, there is no build.
- **Every host is logged.** The sandbox's network log lists every host allowed or blocked
  per build, with a count. Logs are taken before and after the build, so what the sandbox
  does on its own at start is not counted. The negative layer reads it: a build that tries
  a closed host is flagged. Requests to the model proxy are an exception: plain HTTP to the
  same host is counted once, not per request, so the proxy's own log is the record of model
  traffic.
- **Outside state is noise, recorded.** Anonymous pulls from Docker Hub are rate-limited,
  and registries move. A build that fails on either is recorded as such, not retried until
  it passes.

### What the dry run found

Run on 3 November 2026, from a sandbox set up exactly as a build's, with `sbx` v0.50.1:

- One fetch per ecosystem, then a Mosquitto container pulled from Docker Hub and started,
  an MQTT client installed from apt, another Go toolchain, the Gradle distribution, a
  GitHub release, a clone and a Composer archive. All worked.
- Registries redirect: `services.gradle.org` and Packagist archives to GitHub, Go toolchains
  to `storage.googleapis.com`, Docker Hub layers to its CDNs. Each target is listed above.
- This repository was refused by clone, by URL in other cases and by `go get`; the site and
  the general web were refused.

## Gates

Gate order is fixed per version and recorded in the ADR. At H0:

1. **Build.** The whole spec and an empty repo go to the vendor harness, which builds and
   tests as it sees fit until it declares done (`run-h0/prompt.txt`).
2. **Review.** A fresh session of the same harness and model tests every clause and every
   acceptance criterion of the spec against the build (`run-h0/review-prompt.txt`). How it
   tests each one is up to it; it may simulate the devices, as long as they behave as the
   spec describes them. A requirement that is not implemented, or that it cannot test, is a
   high issue; the prompt defines each severity with examples. It runs in a second sandbox,
   prepared like the first, where the repository is mounted read-only from the host's side,
   so it cannot change the code it tests, as root either; a change that got through anyway
   would be recorded, kept as a patch and undone. It lists every issue with a severity, high,
   medium or low, whether it is broken or not tested, the clause, the file and its evidence;
   then every clause it confirmed and every clause it could not, which together must name
   them all.
3. **Fix.** If any issue is high, a fresh session gets the whole review and deals with the
   high issues only (`run-h0/fix-prompt.txt`): it fixes what is broken, and makes sure of what
   could not be tested, in a way the next review can test. Then back to review.
4. **Stop** when no issue is high, or after 5 fix rounds. Every review is a fresh session
   that sees no earlier review, so their counts are not compared. Medium and low issues are recorded, never fixed.
5. **Grade.** The hidden checks grade the result.

The reviewer sees the spec and the code, never the hidden checks or their results, which are
never fed back to the agent. Each later version changes the loop because of a specific
failure, and the ADR names that failure.

## Where numbers go

Numbers belong in the `CHANGELOG.md` row and in the episode, not in the README. The README
describes the current version only. When the current version changes, this file is updated;
the history is the changelog.
