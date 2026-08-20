# Releasing K3k

This document describes how K3k releases are produced, and the steps to follow when a new
release line is started or an old one is retired.

## Conventions

| Thing | Convention | Example |
| --- | --- | --- |
| Release branch | `release/vX.Y` | `release/v1.1` |
| Application tag | `vX.Y.Z`, `vX.Y.Z-rcN` | `v1.2.0`, `v1.2.0-rc4` |
| Chart release tag | `chart-<semver>` | `chart-1.2.0-rc4` |
| Chart version | application version without the leading `v` | `1.2.0-rc4` |

`main` is always the current development line. Once a line is ready to be maintained
separately, a `release/vX.Y` branch is cut from `main` and patch releases are tagged there.

## Release line status

Maintained by hand. Update it whenever a line changes state.

| Line | Branch | Status | Artifacts published |
| --- | --- | --- | --- |
| 1.2 | `main` | Development | Yes |
| 1.1 | `release/v1.1` | Maintenance (security patches only) | No, tags only |
| 1.0 | `release/v1.0` | Maintenance (security patches only) | No, tags only |

"Tags only" means security patches are still tagged, and the GitHub release page still offers
the automatically generated source archives, but no binaries, no container images and no Helm
chart are published for that line.

## Cutting a release

Releases are created from the GitHub UI:

1. Go to **Releases → Draft a new release**.
2. Choose **Create new tag on publish** and enter the tag (`vX.Y.Z` or `vX.Y.Z-rcN`).
3. Select the target branch (`main` for a new minor, `release/vX.Y` for a patch).
4. Write the release notes and publish.

Publishing pushes the tag, which triggers [`.github/workflows/release.yml`](../.github/workflows/release.yml).
That workflow runs [GoReleaser](../.goreleaser.yaml), which builds the `k3k`, `k3k-kubelet` and
`k3kcli` binaries and the two multi-arch container images.

Where the artifacts go depends on how the workflow was triggered:

| Trigger | Registry | Images |
| --- | --- | --- |
| Tag push in `rancher/k3k` | Docker Hub | `rancher/k3k:<tag>`, `rancher/k3k-kubelet:<tag>` |
| `workflow_dispatch`, or any fork | `ghcr.io` | `ghcr.io/<owner>/k3k:<tag>`, `…-kubelet:<tag>` |

Only the exact tag is pushed — there is no `latest` or floating minor tag — so a release from an
older line can never overwrite a newer image.

The Helm chart is released separately by the manually dispatched
[`chart.yml`](../.github/workflows/chart.yml) workflow, which uses `chart-releaser`
(see [`.cr.yaml`](../.cr.yaml)) to create a `chart-<version>` GitHub release and update
`index.yaml` on the `gh-pages` branch. Dispatch it from the branch holding the chart version you
want to publish.

## How the release filter works

`release.yml` only runs for tags matching the globs listed under `on.push.tags`. On `main` today
that is `v1.2.*`.

> [!IMPORTANT]
> For a tag push, GitHub Actions runs the workflow file **as it exists at the tagged commit**,
> that is, from the release branch itself. A tag push carries no branch information in the event
> (`github.ref` is `refs/tags/v1.2.0`), which is why the filter is written as a tag glob rather
> than a branch list.

Two consequences:

- A newly cut `release/vX.Y` branch inherits the glob for its own line, so it keeps publishing
  patch releases with no changes needed on the new branch.
- Editing the glob on `main` does **not** affect branches that have already been cut. Retiring a
  line is always a commit on that line's own branch.

## Starting a new release line

When `main` moves on from `X.Y` to `X.Y+1`:

1. Cut `release/vX.Y` from `main`. Do not touch its `release.yml` — the inherited `vX.Y.*` glob
   is already correct for that branch.
2. On `main`, bump the glob in `.github/workflows/release.yml` to the new line:

   ```yaml
   on:
     push:
       tags:
       - "vX.Y+1.*"
   ```

   Forgetting this means a tag on `main` produces no release run at all, rather than a bad
   publish, but it fails silently — so do it in the same PR as the version bump below.
3. On `main`, bump `version` and `appVersion` in [`charts/k3k/Chart.yaml`](../charts/k3k/Chart.yaml).
4. On `main`, add `release/vX.Y` to `baseBranchPatterns` in
   [`.github/renovate.json`](../.github/renovate.json) so Renovate keeps the new branch updated.
5. Update the release line status table above.

## Retiring a release line

A retired line still receives security patches and is still tagged, but publishes no artifacts.
On that line's branch, remove the `push:` trigger from `.github/workflows/release.yml`:

```diff
 on:
-  push:
-    tags:
-    - "vX.Y.*"
   workflow_dispatch:
     inputs:
       commit:
         type: string
         description: Checkout a specific commit
```



Then:

- Keep the branch in `baseBranchPatterns` in `.github/renovate.json` so Renovate keeps feeding it
  dependency and security bumps.
- Update the release line status table above (on `main`).

> [!WARNING]
> When publishing a release for a retired line, **uncheck "Set as the latest release"** in the
> GitHub UI. It is checked by default, so publishing `v1.1.1` after `v1.2.0` would otherwise park
> a release with no artifacts at the top of the repository page.

## Deleting a bad release

[`release-delete.yml`](../.github/workflows/release-delete.yml) is a manually dispatched workflow
that takes a tag, refuses to run unless the corresponding GitHub release is still a draft, and
then removes the `k3k` and `k3k-kubelet` container versions for that tag along with the release
itself.
