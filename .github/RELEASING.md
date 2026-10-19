# Release runbook

## Normal release

Release from a reviewed commit on `main` by creating and pushing a new semantic
version tag such as `v0.18.4`. The Release workflow intentionally has no manual
trigger: a tag push is the only event that can publish binaries, Homebrew
formulae, and container images.

The verification job runs the Go tests and `govulncheck` before any publishing
job starts. The vulnerability scan is fail-closed by default.

CI and release jobs require Go `~1.26.9`: at least 1.26.9, while accepting
newer 1.26 patches. This security floor is independent of the development
minimum in `server/go.mod`. A plain `1.26.x` can resolve to an older patch
while the `setup-go` version manifest catches up with an official Go release.
When raising the security floor, update all Go workflows and the pinned
Go builder image in `Dockerfile` together so shipped binaries also receive
the fixes.

## Emergency vulnerability-scan bypass

Use the bypass only when `govulncheck` itself or its live vulnerability database
is unavailable, or when maintainers have documented a confirmed false positive
that blocks an urgent release. Never use it to publish a release with an
unresolved reachable vulnerability.

1. Record the reason and maintainer approval in the release issue or pull
   request, and confirm no other release is in progress.
2. In **Settings → Secrets and variables → Actions → Variables**, set the
   repository variable `ALLOW_VULN_BYPASS_FOR_TAG` to the exact release tag,
   for example `v0.18.4`.
3. Re-run the failed Release workflow for that tag. A different tag, an empty
   value, or any typo keeps the scan enabled.
4. Confirm the verification log contains the explicit bypass warning and retain
   the workflow URL in the incident record.
5. Delete `ALLOW_VULN_BYPASS_FOR_TAG` immediately after the release run
   completes. The tag-scoped value prevents a concurrent release with another
   tag from inheriting the bypass.

Every Go binary retains its compiler version in the standard Go build metadata;
use `go version -m <binary>` when auditing a downloaded release artifact.

## Local self-host releases

Fork releases use `.github/workflows/release-local.yml`. Push a fresh patch
prerelease tag such as `v0.6.4-local.1` from the intended fork commit. The upstream
Release workflow excludes `v*-local.*`; do not publish fork artifacts from a
local workstation or move an existing release tag.

Configure repository variables `MULTICA_DOCKER_IMAGE` (one Docker Hub repository,
for example `jlliu0204/multica`) and `MULTICA_SELFHOST_URL` (the HTTPS platform
origin). Configure encrypted Actions secrets `DOCKERHUB_USERNAME`,
`DOCKERHUB_TOKEN`, `MULTICA_ANDROID_KEYSTORE_BASE64`, and
`MULTICA_ANDROID_KEYSTORE_PASSWORD`. Android uses the `multica-local` key alias;
preserve this key across releases so installed APKs can upgrade.

The release gate runs Go tests with the race detector, `govulncheck`, and
web/desktop/shared-view/mobile typechecks, lint and tests. CI then builds:

- Linux AMD64 backend and web images for the cloud host, tagged
  `backend-<tag>` and `web-<tag>` in the same Docker Hub repository.
- macOS ARM64 and x64 DMG/ZIP installers with the matching daemon and bundled
  platform URLs. Explicit `~/.multica/desktop.json` settings still take priority.
- A signed Android release APK with the same version, package
  `ai.multica.mobile.local`, and the workflow run number as its increasing
  version code. Rerunning the same workflow retains the same version code.

Only after all jobs succeed does CI publish the GitHub prerelease, installers,
checksums and a manifest identifying the source commit, CI run and images. A
published release is immutable from this workflow; fixes use a new tag.
The macOS installers are internal, unnotarized builds; users allow the first
launch in Privacy & Security and update manually from the download page.
Their update repository is the fork, never the upstream release feed.

On the cloud host, use a Compose override to select both exact component tags:

```yaml
services:
  backend:
    image: jlliu0204/multica:backend-v0.6.4-local.1
  frontend:
    image: jlliu0204/multica:web-v0.6.4-local.1
    environment:
      MULTICA_RELEASE_REPOSITORY: DuskLin/multica
      MULTICA_RELEASE_CHANNEL: local
```

Pull these images, then run `docker compose ... up -d --no-build`. Preserve the
existing environment and volumes. Record previous image references before
updating so a rollback can select the prior pair. No source builds or agent
execution belong on the cloud host.

Acceptance requires a successful CI run, matching running image digests,
HTTPS `/readyz`, login, daemon WebSocket routing, and `/download` links pointing
to the complete matching fork release. Verify downloaded checksums and package
metadata. CI packaging and static validation do not establish native UI or
real-agent execution coverage; record those checks separately.
