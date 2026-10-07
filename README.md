# spike-s3-storage

A one-off spike: an S3 provider for [go-storage](https://github.com/standards-lab/go-storage)
v0.4.0, built on aws-sdk-go-v2 and validated against SeaweedFS's S3 gateway, and the blobfs CLI
of spike-cli-architecture ported onto it. The `s3` module depends on published versions only;
the `app` module finds `s3` through `go.work`. Nothing here has a `replace` directive, and
nothing is published.

## The question

Does go-storage's `Client` interface hold over S3, using an aws-sdk-go-v2 provider validated
against SeaweedFS 4.48's S3 gateway — and what does SeaweedFS's S3 change for the adapter? And
does a real go-storage consumer — the blobfs CLI over Postgres, ported from
spike-cli-architecture — run unchanged on the s3 provider?

## Evidence

Test transcripts are in `evidence/`, all captured from one commit: `check.txt` (`mise run
check`), `acceptance.txt` (`GOFLAGS=-v mise run acceptance` against `mise run seaweedfs:start`),
`integration-run-1.txt` and `-2.txt` (`GOFLAGS=-v mise run app:integration`, twice), and
`scenario-directories.txt` and `scenario-files.txt` (each scenario run twice against the
development stack).

1. `storagetest.Run` passes against SeaweedFS 4.48's S3 gateway. **Met**:
   `TestAcceptance_Conformance`, all 15 subtests, in `acceptance.txt`.
2. `storagetest.RunMissingContainer` passes. **Met**: `TestAcceptance_MissingContainer`,
   `acceptance.txt`.
3. `Store.Start` creates a missing bucket, probes, and reports ready; an unreachable endpoint
   yields `ErrUnavailable`. **Met**: `TestAcceptance_StoreStart` and
   `TestAcceptance_StoreStartUnreachable`, `acceptance.txt`.
4. `EnsureContainer` on an existing bucket succeeds and changes nothing. **Met**:
   `TestAcceptance_EnsureContainerExisting`, `acceptance.txt`.
5. A `Put` above the part size is invisible to `Stat`/`List` until complete; a body failing
   partway leaves no object and no open multipart upload. **Met**:
   `TestAcceptance_MultipartInvisibleUntilComplete` (12 MiB at 5 MiB parts, listed by
   ListMultipartUploads, unseen by `Stat` and `List`, then a `"<hex>-3"` ETag) and
   `TestAcceptance_MultipartFailureLeavesNothing` (body failure, short declared size,
   cancellation, each after the upload is listed), `acceptance.txt`.
6. `Stat` on a missing bucket yields `ErrContainerNotFound`, never `ErrNotFound`. **Met**:
   `TestAcceptance_StatMissingBucket`, `acceptance.txt`.
7. Error classification, ETag quoting, and key validation hold against a scripted S3 stand-in
   — unit tests only. **Met**: `TestObjectOperations_ClassifyErrors`,
   `TestObjectOperations_UnreachableEndpointIsUnavailable`, `TestStat_HeadBucketDisambiguatesA404`,
   `TestETag_EntityTagForm`, `TestValidateKey_Accepts`/`_Rejects`, and the other `s3` unit tests,
   in `check.txt` and verbosely in `acceptance.txt`.
8. The SeaweedFS-vs-AWS differences the provider absorbs, or that only AWS docs support.
   **Finding, no test**: [SeaweedFS compared with AWS S3](#seaweedfs-compared-with-aws-s3).
9. spike-cli-architecture at its validate merge (2f11b46) is ported as module `./app`, and only
   the store constructor, the stack, the endpoints, and the relay change. **Met**: `diff -r`
   of the source at 2f11b46 against `app/` at 53f7edd differs in module-path lines only; every
   later change is listed under [The port](#the-port); `check.txt` passes for `.`, `s3`
   (`GOWORK=off`) and `app` (workspace mode).
10. The integration suite and both scenarios pass on Postgres plus SeaweedFS 4.48. **Met**: 240
    top-level tests (655 with subtests) pass in each of `integration-run-1.txt` and `-2.txt`,
    `TestScript`, `TestScenarios`, and `TestTheStoreUnreachable` among them; each scenario exits
    0 twice in `scenario-directories.txt` and `scenario-files.txt`.
11. Bodies above the part size round-trip through the CLI, and SIGINT mid-upload cleans up.
    **Met**: `TestALargePut` (file and stdin, 15 MiB less one byte at 5 MiB parts, `cat`'s
    SHA-256 matches, ETag ends `-3`, no open upload) and `TestAnInterruptedLargePut` (exit 1,
    one cancellation report, no object, no open upload), `integration-run-1.txt` and `-2.txt`.
12. SIGKILL mid-upload, then resume. **Finding**: `TestACrashedLargePut` shows a pending row and
    one open upload after the kill; the resumed put completes through an upload of its own, and
    the crashed one stays open after the resume and after `rm`, in `integration-run-1.txt` and
    `-2.txt`. See [Findings](#findings).

## The answer

**Yes: the `Client` interface holds over S3 — the provider passes go-storage's conformance
suite on SeaweedFS 4.48 with no SeaweedFS-specific code path, and blobfs runs on it with only
its store constructor changed; the gap is an orphaned multipart upload after a crash.**

- Evidence 1–7 are met: conformance, the missing-container suite, `Store.Start`,
  `EnsureContainer`, multipart visibility and abort, and `Stat` on a missing bucket all pass
  against the gateway; classification, ETag quoting, and key rules pass against the stand-in.
- SeaweedFS changes the harness, not the adapter: `-s3.autoCreateBucket=false` and a signed
  readiness check. Its other differences from AWS are either standard-conforming variants the
  adapter would handle anyway or looser checks the adapter never relies on (evidence 8).
- Evidence 9–11 are met: blobfs ports with one import and one constructor call changed, and
  its whole integration suite, both scenarios, and large bodies through the multipart path pass.
  The only test-side change S3 forced was the fault relay forwarding the signed `Host`.
- Evidence 12 is a finding: a `put` killed mid-upload leaves an upload no one can name.

## SeaweedFS compared with AWS S3

Observed against SeaweedFS 4.48 (`weed mini`); the AWS side is from AWS's documentation, since
no live AWS run is in scope.

| Behavior | SeaweedFS 4.48 | AWS S3 | Effect on the provider |
| --- | --- | --- | --- |
| Owner re-creates a bucket | 409 `BucketAlreadyOwnedByYou` | 200 in us-east-1, 409 elsewhere | Both are success; `BucketAlreadyExists` is success only if a HeadBucket follows OK |
| Write to a missing bucket | Created on an admin's upload (`-s3.autoCreateBucket` defaults on) | `NoSuchBucket` | Harness and compose turn it off; with it on, evidence 6 cannot be shown |
| Signature region | Any region accepted | The bucket's region required | `region` option, default us-east-1 |
| Reserved bucket names | `sthree-x` accepted | Rejected | `New` rejects them |
| Admin identity, readiness | From `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` at startup; the port answers before the credential loads | IAM | Readiness is a signed ListBuckets |
| Object ETag headers | Adds a nonstandard unquoted `Seaweed-X-Amz-ETag` | Standard `ETag` only | Ignored |
| Listing `LastModified` | Whole seconds | `.000Z` milliseconds | Truncated to the second everywhere |
| `NextContinuationToken` | The page's last key | Opaque | Passed back verbatim, never read |
| Abort of an aborted or completed upload | Success | `NoSuchUpload` | Repeat abort treats `NoSuchUpload` as success |
| Wrong `x-amz-mp-object-size` | Ignored | Rejected | Not relied on; the provider checks the size itself |
| Part below 5 MiB | `EntityTooSmall`, as AWS | `EntityTooSmall` | `part_size` minimum 5 MiB |
| Storage | 1 GiB volume limit, one volume per bucket on first write | — | No limit reached in any run |

What matches AWS: HeadBucket's bare 404 on a missing bucket, `NoSuchBucket` on object
operations, `InvalidBucketName`, HeadObject's bare 404 for a missing key and a missing bucket
alike, no `Last-Modified` in PutObject's or CompleteMultipartUpload's answer, multipart
ETags of the form `"<hex>-<parts>"`, ListMultipartUploads, CRC32 part checksums, and versioning.
One timing is unexplained: cleanup after a body fails midway took about 1.1 s.

## Findings

- **A SIGKILLed large `put` orphans its multipart upload.** blobfs keeps no upload ID, and
  neither does the provider once the process dies, so a resumed `put` starts a new upload and
  `rm` cannot reach the old one (`TestACrashedLargePut`). Only a bucket lifecycle rule
  (`AbortIncompleteMultipartUpload`) or a ListMultipartUploads sweep frees it. On AWS the
  orphan's parts are billed until then. SIGINT is clean (`TestAnInterruptedLargePut`).
- **SigV4 signs `Host`, so the host-rewriting fault relay broke.** The integration suite's
  relay rewrote `Host` to the store's, as `httputil.ReverseProxy`'s `SetURL` does, and every
  relayed request failed `SignatureDoesNotMatch`. Azure SharedKey doesn't sign the host, which
  is why it worked against Azurite. The relay now forwards the incoming `Host`.
- **transfermanager drops a failed abort's error and needs `FailTimeout` to abort on
  cancellation.** It aborts the upload itself after a body, part, or completion failure, but
  after the caller's cancellation only when `FailTimeout` gives the abort a fresh context, which
  the provider sets. It drops the abort's error when the abort fails, so the provider sends a second
  AbortMultipartUpload for the same upload ID and names it in `Put`'s error if it still fails
  (`TestPut_FailedAbortIsRepeated`, `TestPut_CancelledMultipartStillAborts`).
- **transfermanager writes to the standard `log` package** when a completion fails, outside
  the caller's logger.

## Porting into go-storage — prerequisites

The provider is not ready to move into go-storage as it stands:

- **No `try_timeout`.** azureblob bounds each try with a per-try deadline and resumes a `Get`
  past it; the provider has neither, and go-storage's timeouts are standard.
- **The read-ahead bound is not stated.** A multipart `Put` holds up to part size ×
  transfermanager's default concurrency in memory; the provider has no concurrency option and
  doesn't document the product.
- **No `CHANGELOG.md`** for the `s3` module; it is written at port time.
- **A cancelled `Put`'s error lacks the `s3:` prefix** the others carry.
- **A v0 dependency.** transfermanager v0.4.14 is held as a stated exception, as
  go-observability holds otelhttp (`s3/doc.go`, Dependencies); the port must accept that.
- **go-storage's `context/provider-assumptions.md` can be updated.** Its three unexercised S3
  claims now have evidence against SeaweedFS: `CreateBucket`'s existing-bucket result
  (`TestAcceptance_EnsureContainerExisting`, `TestEnsureContainer_AlreadyOwnedByYou`), multipart
  visibility (`TestAcceptance_MultipartInvisibleUntilComplete`), and `Stat` on a missing bucket
  through HeadBucket (`TestAcceptance_StatMissingBucket`, `TestStat_HeadBucketDisambiguatesA404`).

## The port

Source: spike-cli-architecture at 2f11b46, its validate merge. 53f7edd copies it into `./app`
with the module path rewritten to `github.com/JaimeStill/spike-s3-storage/app`; `diff -r`
against the source shows module-path lines only. Not copied: `context/`, `.claude/`,
`CLAUDE.md`, `.gitignore`, `scripts/`, and `mise.toml`. Every changed line after 53f7edd:

- `app/internal/app/infrastructure.go`: the import of `go-storage/azureblob` becomes
  `spike-s3-storage/s3`, `azureblob.New(cfg)` becomes `s3.New(cfg)`, and `newStore`'s doc names
  the bucket, keys, and s3 options. The integration probe
  (`infrastructure_integration_test.go`) makes the same swap.
- Three test endpoint strings drop Azurite's `/devstoreaccount1` path.
- `app/doc.go`: the root package `spikecliarchitecture` becomes `app`.
- `app/go.mod`: `go-storage/azureblob` and the Azure-only indirect requirements are removed.
  It has no `s3` requirement: Go 1.27.1 rejects a require of the zero pseudo-version
  without a `replace` ("unknown revision"), so `go.work` alone resolves `s3`, and nothing is
  published. Its check runs in workspace mode and skips `go mod tidy -diff`.
- Compose: moved to the repository root; Azurite becomes SeaweedFS 4.48 (`mini
  -s3.autoCreateBucket=false`, admin/secret, healthy on a signed listing); ports 5438/8334 for
  development and 5439/8335 for integration.
- The integration fault relay: its delete predicate becomes `objectDelete` (a path-style
  DELETE of `/<bucket>/<key>` without `uploadId`), and it forwards the incoming `Host` for SigV4.
- `mise.toml`: the `app:*` tasks, `BLOBFS_*` defaults for the development stack, and `app` in
  the check as a workspace module; `go.work` gains `./app`; `scripts/currency.sh` covers it.
- `app/USAGE.md` and `app/STANDARDS.md` adapted to this repository, USAGE's outputs
  re-captured against SeaweedFS.
- Added: `app/integration/largebodies_test.go` (evidence 11–12), which reads the bucket through
  its own S3 client — an exception `app/integration/doc.go` states — and `proc`'s quiet mode,
  which logs a large stdout as its length and SHA-256.

## Running it

| Task | What it does |
| --- | --- |
| `mise run check` | Build, vet, format, fix, tidy, test, and lint every module; writes nothing |
| `mise run seaweedfs:start` / `seaweedfs:stop` | Start or stop the acceptance harness on 127.0.0.1:8333 |
| `mise run acceptance` | The `s3` tests with the acceptance tests on, against the running harness |
| `mise run app:up` / `app:down` / `app:reset` | The development stack: Postgres 5438, SeaweedFS S3 8334; reset drops its data |
| `mise run app:integration` | An isolated stack on 5439/8335, the app's integration suite, then teardown |

`app/USAGE.md` walks through blobfs against the development stack.
