# Publishing signatures

This page is for people who publish a cpak package. It explains what a package
signature is, what you add to your workflow to produce one, what it costs you,
and what happens to the people who install your package if you never do it.

## Doing nothing keeps working

Signing is optional and it stays optional. A package with no signature installs
and runs exactly as it does today, and nothing in this page changes that.

What you give up by not signing is the only thing a signature adds: nobody can
tell a build that came out of your repository apart from a copy of it that came
out of somewhere else, and a machine that wants to check before installing has
nothing to check.

## What is signed, and what it proves

A signature does not cover the image. It covers the part of your package's
identity you can determine before it reaches anybody's machine:

- the origin, which is the repository your manifest is published from
- the SHA-256 of your manifest
- the digest of the image that manifest resolved to
- the SHA-256 of your lock, when the package has one
- a generation, which orders two signed states of the same package

The manifest is inside the signature because that is where the permissions
live. Signing only the image would let someone swap `cpak.json` for one that
widens the sandbox and keep a valid signature.

It proves the package came from the CI of that repository and was not altered
on the way. It does not prove the software is safe, and it does not defend
against a compromised repository: the repository is the identity being proven.

## What it costs you

- No key. Signing is keyless through the OIDC identity of your CI, so there is
  nothing to generate, store, rotate or lose.
- No secret. The workflow below uses `secrets.GITHUB_TOKEN`, which GitHub
  creates for every run. You do not add anything to the repository settings.
- Around twenty seconds of workflow time per publish.
- One number that only goes up, and one signing step per publish. A new
  manifest or a new image is a new state, and an old state cannot stand in for
  it.

## The workflow

Add this job to the workflow that already pushes your image, or paste it whole
and point the build step at your `Dockerfile`.

```yaml
name: Publish

on:
  push:
    branches: [main]

permissions:
  contents: read
  packages: write
  id-token: write

jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5

      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - uses: docker/build-push-action@v6
        with:
          push: true
          tags: ghcr.io/${{ github.repository }}:main

      - uses: sigstore/cosign-installer@v3

      - name: Install cpak-sign
        run: |
          curl -fsSLO https://github.com/Containerpak/cpak/releases/latest/download/cpak-sign-linux-amd64
          install -Dm755 cpak-sign-linux-amd64 /usr/local/bin/cpak-sign

      - name: Sign the package state
        env:
          CPAK_REGISTRY_USERNAME: ${{ github.actor }}
          CPAK_REGISTRY_PASSWORD: ${{ secrets.GITHUB_TOKEN }}
        run: |
          cpak-sign state \
            --origin "github.com/${{ github.repository }}" \
            --image "ghcr.io/${{ github.repository }}:main" \
            --generation "${{ github.run_number }}"
          cosign sign-blob --yes --new-bundle-format=true \
            --bundle cpak-state.sigstore.json cpak-state
          cpak-sign attach --image "ghcr.io/${{ github.repository }}"
```

The three lines that do the work are the three in the last step. The first
writes the payload, the second signs it with the identity of this workflow run,
the third attaches the result to the image in your registry.

`permissions: id-token: write` is what lets the run prove who it is. Without it
`cosign` has no identity to sign with and the step fails.

## What is in the payload

The payload is a short text file, one field per line, and it is the exact byte
string that gets signed. You can read it:

```
cpak.signature.state.v1
abi=1
origin=github.com/example/app
manifest_sha256=6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b
image_digest=sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae
lock_sha256=
generation=12
```

Nothing in it is a secret, and nothing in it is about the machine that will
install the package. `cpak verify-signature` takes the same fields and checks a
bundle against them by hand, which is how you confirm what you published
without installing anything.

## The generation

`--generation` orders two signed states of the same package, and cpak uses it
to tell a newer state from an older one. `github.run_number` is a reasonable
source: it goes up by one on every run of the workflow.

It has to keep going up. Renaming or replacing the workflow file restarts
`run_number` at 1, so if you rename it, switch to a number that carries on from
where the last publish left off.

## Tags, and why the digest is what gets signed

A signature over a tag would be worth nothing: a tag can be repointed at
another image the day after it is signed. So `cpak-sign state` resolves the
reference you give it and puts the digest it resolved to inside the payload.
The signature is the pin.

This is why `image_ref: source` needs no change. Keep publishing a tag per
branch, release and commit as you do now. Each publish signs the digest that
tag currently points at, and a tag that is later moved to another image no
longer matches what the signature states.

`cpak-sign state` refuses a reference that is not a digest in `--image-digest`,
loudly, rather than signing something that can move under it.

## More than one architecture

cpak measures the image manifest for the architecture it is installing on, so a
multi-architecture image needs one signed state per architecture. Run the
signing step on a runner of each architecture:

```yaml
    strategy:
      matrix:
        runner: [ubuntu-latest, ubuntu-24.04-arm]
    runs-on: ${{ matrix.runner }}
```

Each run resolves the same tag to the manifest for its own architecture, signs
that digest, and attaches the bundle to that manifest. A single architecture
image needs none of this.

## What your users get

The bundle travels with the image, and it carries the certificate and the
transparency log proof inside it. Verification is offline: cpak checks it
against a trust root that ships with cpak, so it adds no network call to the
download, it keeps working during a Sigstore outage, and it still works later
on a machine with no internet at all, which is what lets a package be checked
again long after it was installed.

## Reformatting, and what does not break a signature

The manifest hash is taken over the JSON cpak itself encodes your manifest as,
not over the bytes of your file. Reindenting `cpak.json` or reordering its keys
does not invalidate a signature. Changing a permission, an image, a binary or a
dependency does, and that is the point.

If a `cpak.lock.json` sits beside the manifest, `cpak-sign state` includes its
hash and refuses to sign when the lock was built from a different manifest. Run
`cpak lock cpak.json` again and commit the result.

## Command reference

`cpak-sign state` builds the payload:

| Flag             | Meaning                                                        |
| ---------------- | -------------------------------------------------------------- |
| `--manifest`     | Path to the manifest. Defaults to `cpak.json`.                   |
| `--lock`         | Path to the lock. Defaults to `cpak.lock.json` beside the manifest, when it exists. |
| `--origin`       | The repository the manifest is published from.                   |
| `--image`        | The reference to resolve. Defaults to the image the manifest declares. |
| `--image-digest` | A digest to sign as it is, for a registry the run cannot reach.  |
| `--generation`   | This state's generation. Starts at 1.                            |
| `--output`       | Where the payload is written. Defaults to `cpak-state`, `-` for standard output. |

`cpak-sign attach` publishes the bundle:

| Flag       | Meaning                                                       |
| ---------- | ------------------------------------------------------------- |
| `--image`  | The repository the signed image lives in.                      |
| `--state`  | The payload that was signed. Defaults to `cpak-state`.         |
| `--bundle` | The bundle `cosign` wrote. Defaults to `cpak-state.sigstore.json`. |

`attach` reads the image digest out of the signed state, so it can only ever
publish against the image the signature covers. It verifies the bundle before
it pushes anything, and it refuses a bundle signed by an identity that cannot
speak for your origin, because that is a signature every user would reject.

`CPAK_REGISTRY_USERNAME` and `CPAK_REGISTRY_PASSWORD` are how both commands
authenticate to the registry. A password is never a flag.

## Registries

The signature is attached as an OCI referrer of the image, which is what
`cosign` does natively and what GHCR supports. `attach` fails if the registry
stores the manifest without indexing it as a referrer, because cpak finds a
signature through the referrers API and a signature nobody can find is not a
published signature.

## Building cpak-sign yourself

If you would rather not download a binary:

```sh
git clone --depth 1 --branch v2 https://github.com/Containerpak/cpak /tmp/cpak
go -C /tmp/cpak build -o /usr/local/bin/cpak-sign ./cmd/cpak-sign
```
