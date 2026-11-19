# builds/

The source of every accepted build: the `heatctl` that has passed its soak in the house. Each
directory is named after its run id and holds the build's repository as the harness left it,
at its last commit, or, where the build committed nothing, as its working tree. Nothing in it is edited, not even formatting: no code here is written by
hand.

Only accepted builds are published. The others are archived with their run, and their
results are in the version's row of `harness/CHANGELOG.md`.

| Build | Version | Model | Commit | Accepted | Notes |
|---|---|---|---|---|---|
| [`h0-20261104T165650Z-c300`](h0-20261104T165650Z-c300/) | H0, [ADR 001](../harness/adr/001-baseline.md) | Claude Code with Sonnet 5.5 | `1787b93`, "Implement heatctl", on `97f5f5e`, "spec" | 10 November 2026 | build 3 of 3; ran in the house until 12 November, kept there to roll back to |
| [`h2-20261111T232420Z-0559`](h2-20261111T232420Z-0559/) | H2, [ADR 003](../harness/adr/003-the-model.md) | Gemini 4 Carbon, the one-line instructions | `df3f487`, "spec", with the build uncommitted: the working tree as the harness left it | 15 November 2026 | chosen by the code comparison over the other eligible builds and `c300`; ran in the house until 15 November, kept there to roll back to |
| [`h0-20261113T121238Z-7d99`](h0-20261113T121238Z-7d99/) | H0, [ADR 001](../harness/adr/001-baseline.md), on a Claude subscription: Claude Code 2.1.311 by `HEATCTL_TEMPLATE`, the model reached through [`harness/subscription-proxy.py`](../harness/subscription-proxy.py) | Claude Code with Opus 5.5 | `015bf05`, the build session's working tree as `h0-20261113T105851Z-800f` left it, on `0c0d79a`, "spec"; its review asked for no change | 18 November 2026 | won every pair of the code comparison; runs in the house (`fixture/DEPLOYED.md`) |

## Reading a build

- `SPEC.md` in each build is the spec it was given, a copy of `spec/SPEC.md` at that version.
- `README.md` is the build's own, written by the agent; how to install and run that build.
- `c300`'s binary is the one the harness built for that run, `heatctl 1.0.0`,
  `dist/heatctl-linux-arm64`, sha256
  `b945e4ca10d5fe6d6c70da031bc7b866278447e0d75f80b12e4f75dab0f35584`. Go embeds build paths
  unless told not to, so a rebuild elsewhere gives a different hash.
- `0559`'s binary was built from this source for the house, `CGO_ENABLED=0 GOOS=linux GOARCH=arm64
  go build -trimpath ./cmd/heatctl` with Go 1.27.3, sha256
  `58dc12c9da0ed6a2c980994ccd47c87926cafb832e45be3b4c832263529f1fb9`; `-trimpath` leaves the paths out,
  so the same command and Go version give the same hash.
- `7d99`'s binary was built from this source for the house, `CGO_ENABLED=0 GOOS=linux GOARCH=arm64
  go build -trimpath -ldflags "-s -w -X main.version=1.0.0" ./cmd/heatctl` with Go 1.27.3, sha256
  `9dfdd57d6bc2445767b368f066ed9dd18092b0836073b68a93dd7cd060e3a655`.
