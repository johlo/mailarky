# Publish a release

The [Release workflow](../../.github/workflows/release.yml) runs when a tag
such as `v0.1.0` is pushed. It reuses the normal CI workflow, including race
tests, `go vet`, and the Docker SMTP/IMAP smoke test, before publishing.

## Prepare the repository

Merge the changes to release into `main`. The workflow uses the automatic `GITHUB_TOKEN` with `contents: write` and
`packages: write`; it needs no additional secret or Docker Hub account.

If a `mailarky` package already exists in GHCR, connect it to this repository
and grant this repository Actions access in the package settings.

## Publish a version

From a clean checkout of the commit you want to release:

```sh
git switch main
git pull --ff-only
git tag -a v0.1.0 -m 'Mailarky v0.1.0'
git push origin v0.1.0
```

Use a new version for every release. The workflow rejects an already published
GitHub release. It builds the tagged commit, so merge all intended changes
before creating the tag. Follow the run in the repository's **Actions** tab.

The workflow publishes:

- `ghcr.io/johlo/mailarky:v0.1.0`, with Linux AMD64 and ARM64 in one image.
- `ghcr.io/johlo/mailarky:latest` after a successful stable release.
- Six binary archives: Linux, macOS (`darwin`), and Windows, each for AMD64
  and ARM64. They include the executable, MIT license, documentation, and
  public TLS test fixtures.
- `checksums.txt` with SHA-256 hashes, plus a separate `server.crt` download.
- A GitHub release with installation notes and automatically generated changes.

For a prerelease, use a tag such as `v0.1.0-rc.1`. It is marked as a prerelease
on GitHub and gets its own Docker tag; it does not update `latest`.

## Make the first image public

GitHub makes a new GHCR package private by default, independently of repository
visibility. After the first successful image publication, open the
[`mailarky` package](https://github.com/users/johlo/packages/container/package/mailarky),
choose **Package settings**, and change its visibility to **Public**.
See [GitHub's container registry documentation](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry#pushing-container-images).

Verify an anonymous pull from a machine or Docker configuration that is not
logged into GHCR:

```sh
docker pull ghcr.io/johlo/mailarky:v0.1.0
docker run --rm ghcr.io/johlo/mailarky:v0.1.0 --version
```

The binary and Docker image report the release version and Git commit. The
[quick start](../../README.md#quick-start) then works without a source checkout.

## Validate packaging locally

Install [GoReleaser v2.18.2](https://goreleaser.com/install/), then run:

```sh
goreleaser check
goreleaser release --snapshot --clean --skip=publish
```

This builds all six archives and their checksums under the ignored `dist/`
directory. It does not create a tag, upload artifacts, or publish a release.
Review `.goreleaser.yaml` when changing supported platforms or archive contents.

To test the Docker build and version metadata locally:

```sh
docker buildx build --load -t mailarky:release-check \
  --build-arg VERSION=v0.1.0-test \
  --build-arg REVISION="$(git rev-parse HEAD)" .
docker run --rm mailarky:release-check --version
```

## Recover a failed publication

If the GitHub release is still a draft, rerun the failed job; it replaces the
draft's assets before publishing. A Docker version tag can already exist if a
later step failed. Do not move the Git tag to another commit to repair a release.

If only the final `latest` update failed after the GitHub release was published,
log into GHCR with package write access and promote the existing image:

```sh
docker buildx imagetools create \
  --tag ghcr.io/johlo/mailarky:latest ghcr.io/johlo/mailarky:v0.1.0
```

Run that command only for the stable version that should be `latest`.
