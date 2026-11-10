# builds/

The source of every accepted build: the `heatctl` that has passed its soak in the house. Each
directory is named after its run id and holds the build's repository as the harness left it,
at its last commit. Nothing in it is edited, not even formatting: no code here is written by
hand.

Only accepted builds are published. The others are archived with their run, and their
results are in the version's row of `harness/CHANGELOG.md`.

| Build | Version | Model | Commit | Accepted | Notes |
|---|---|---|---|---|---|
| [`h0-20261104T165650Z-c300`](h0-20261104T165650Z-c300/) | H0, [ADR 001](../harness/adr/001-baseline.md) | Claude Code with Sonnet 5.5 | `1787b93`, "Implement heatctl", on `97f5f5e`, "spec" | 10 November 2026 | build 3 of 3; runs in the house (`fixture/DEPLOYED.md`) |

## Reading a build

- `SPEC.md` in each build is the spec it was given, a copy of `spec/SPEC.md` at that version.
- `README.md` is the build's own, written by the agent; how to install and run that build.
- The binary in the house is the one the harness built for that run, `heatctl 1.0.0`,
  `dist/heatctl-linux-arm64`, sha256
  `b945e4ca10d5fe6d6c70da031bc7b866278447e0d75f80b12e4f75dab0f35584`. Go embeds build paths
  unless told not to, so a rebuild elsewhere gives a different hash.
