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
  run/                  current implementation: at H0 a wrapper around a vendor coding harness
                        in a Docker Sandbox: build.sh, prompt.txt, allowlist.txt
```

H0 has no history store and no run records: the vendor harness prints to stdout and that is
all we get. Directories are added here when a version needs them.

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
harness/run/build.sh [SPEC]
```

One run is one build. `build.sh` creates a fresh Docker Sandbox (`sbx`, one microVM per
sandbox) with the claude agent and an empty repository holding only `SPEC.md`, gives it the
network allowlist below and the model key, runs Claude Code headless with `prompt.txt`
until it declares done, then archives the repository, Claude Code's output stream, the
sandbox's network log and a `meta.json` with cost, turns and times, and removes the
sandbox. `HEATCTL_*` variables at the top of the script set the model, the timeout, the
archive and where the key comes from.

The sandbox mounts the repository read-write and nothing else from the host. The agent has
no route to the sandbox daemon, so it cannot change its own policy. The host's global
sandbox policy is deny-all; the allowlist is added for each build's sandbox alone, and the
run refuses to start if `github.com` or `example.com` is reachable from it. H0 does not
claim this is the right sandbox; the ADR records that it was adopted as a safe default, to
be justified or replaced once the runs show what the agent touches.

### Network allowlist

The container has no route out except the proxy. The proxy allows these domains and refuses
everything else:

| Ecosystem | Domains |
|---|---|
| Go | `proxy.golang.org`, `sum.golang.org` |
| Python | `pypi.org`, `files.pythonhosted.org` |
| Node | `registry.npmjs.org`, `registry.yarnpkg.com` |
| Rust | `index.crates.io`, `static.crates.io`, `crates.io`, `static.rust-lang.org` |
| Java and Kotlin | `repo.maven.apache.org`, `repo1.maven.org`, `plugins.gradle.org`, `services.gradle.org` |
| .NET | `api.nuget.org` |
| Ruby | `rubygems.org`, `index.rubygems.org` |
| PHP | `repo.packagist.org` |
| Model endpoint | the API the harness itself talks to, for example `api.anthropic.com` at H0. The sandbox's agent kit adds a few hosts of the vendor's own |

Rules that go with it:

- **Registries only, no code hosts.** `github.com` and other code hosts stay closed. Go uses
  the module proxy with no direct fallback, so modules hosted on GitHub still resolve. A
  build that needs something only a code host has fails, and that is a finding.
- **No OS package mirrors.** System packages, such as an MQTT broker from apt, are not
  reachable. A build that wants a broker for its own tests must get one through a language
  registry, or do without. Either is recorded, not helped.
- **One credential, outside the sandbox.** The model API key is the only secret a build
  can use, and it never enters the sandbox: the sandbox proxy adds it to requests to the
  model endpoint. It is a dedicated key with a spend limit, used for nothing else. No other
  credentials, no SSH keys, no registry tokens, so nothing can be published. The run
  refuses to start if any secret is shared with every sandbox.
- **Every host is logged.** The sandbox's network log lists every host allowed or blocked
  per build, with a count. Logs are taken before and after the build, so what the sandbox
  does on its own at start is not counted. The negative layer reads it: a build that tries
  a closed domain is flagged.
- **Verify with a dry run.** Before episode 1, fetch one package per ecosystem through the
  proxy. Registries change where they serve files from, and a redirect to an unlisted CDN
  shows up as a refused request.

## Gates

Gate order is fixed per version and recorded in the ADR. At H0 it is minimal: hand the
whole spec and an empty repo to the vendor harness, let it build and test as it sees fit
until it declares done, then grade the build with the hidden evals. Eval results are never
fed back to the agent. Each later version changes the loop because of a specific failure,
and the ADR names that failure.

## Where numbers go

Numbers belong in the `CHANGELOG.md` row and in the episode, not in the README. The README
describes the current version only. When the current version changes, this file is updated;
the history is the changelog.
