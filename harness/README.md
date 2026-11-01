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
# placeholder, filled in from H0 onwards
./run/build.sh spec/SPEC.md
```

The loop runs inside a container from H0: repo mounted read-write, no docker socket, and
network only through an egress proxy with a domain allowlist. Nothing else on the host is
reachable. H0 does not claim this is the right sandbox; the ADR records that it was adopted
as a safe default, to be justified or replaced once the runs show what the agent touches.

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
| Model endpoint | the API the harness itself talks to, for example `api.anthropic.com` at H0 |

Rules that go with it:

- **Registries only, no code hosts.** `github.com` and other code hosts stay closed. Go uses
  the module proxy with no direct fallback, so modules hosted on GitHub still resolve. A
  build that needs something only a code host has fails, and that is a finding.
- **No OS package mirrors.** System packages, such as an MQTT broker from apt, are not
  reachable. A build that wants a broker for its own tests must get one through a language
  registry, or do without. Either is recorded, not helped.
- **One credential.** The model API key is the only secret in the container, so the harness
  can call the model. It is a dedicated key with a spend limit, used for nothing else. No
  other credentials, no SSH keys, no registry tokens, so nothing can be published.
- **Every request is logged.** The proxy log lists every domain and package fetched per build.
  It goes into the run records, and the negative layer reads it: a build that tries
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
