# Fuzzing

This repository ships Go native fuzz targets (`testing.F`) across the highest-value
parse, decode, crypto, and verification surfaces. Each target pairs a fuzzed input
with an explicit oracle — a property that must hold for every input — so the fuzzer
finds correctness bugs, not just crashes.

## Targets

| Package | Target | Oracle |
|---------|--------|--------|
| `.` (root) | `FuzzValidateSubject` | no panic for arbitrary subjects |
| `.` (root) | `FuzzValidateBody` | no panic for arbitrary bodies |
| `.` (root) | `FuzzValidateMIMEType` | no panic parsing MIME types |
| `.` (root) | `FuzzValidateHeaders` | no panic; multi-key maps reach the `MaxHeaderCount` / `MaxHeadersTotalSize` branches |
| `.` (root) | `FuzzValidateMetadata` | no panic; multi-key maps reach the `MaxMetadataKeys` / `MaxMetadataSize` branches |
| `.` (root) | `FuzzValidateRecipients` | no panic; lists reach the empty / `MaxRecipientCount` / empty-ID branches |
| `content` | `FuzzBinaryCodecRoundTrip` | **round-trip:** `Decode(Encode(b)) == b` for the base64 binary codec |
| `content` | `FuzzDecode` | no panic for arbitrary body + content-type through `content.Decode` |
| `crypto` | `FuzzDecryptMalformed` | `Decrypt` of random bytes returns an error, never panics, never silently returns a plaintext |
| `crypto` | `FuzzSealOpenRoundTrip` | **round-trip:** `open(seal(pt)) == pt` for the X25519 envelope |
| `notify/webhook` | `FuzzVerifySignature` | `VerifySignature` is true IFF the digest equals the recomputed HMAC (within the documented `sha256=` prefix contract) |
| `notify/webhook` | `FuzzVerifySignatureFreshness` | a correctly-signed payload verifies IFF its timestamp is within tolerance |
| `rules` | `FuzzCompile` | no panic compiling/evaluating arbitrary CEL expressions |

## Running a target

Fuzz targets run as ordinary unit tests over their seed corpus with `go test`.
To actively fuzz a single target:

```bash
# Fuzz one target for 15 seconds, then stop.
go test -run '^$' -fuzz='^FuzzBinaryCodecRoundTrip$' -fuzztime=15s ./content

# Other examples:
go test -run '^$' -fuzz='^FuzzDecryptMalformed$'   -fuzztime=15s ./crypto
go test -run '^$' -fuzz='^FuzzSealOpenRoundTrip$'   -fuzztime=15s ./crypto
go test -run '^$' -fuzz='^FuzzVerifySignature$'     -fuzztime=15s ./notify/webhook
go test -run '^$' -fuzz='^FuzzCompile$'             -fuzztime=15s ./rules
go test -run '^$' -fuzz='^FuzzValidateHeaders$'     -fuzztime=15s .
```

`-run '^$'` skips the normal unit tests so only the fuzz engine runs.
Only one `-fuzz` target can run per `go test` invocation.

## Where seeds live

Seed corpus files live under `testdata/fuzz/<Target>/` next to each package, in
Go's corpus format:

```
content/testdata/fuzz/FuzzBinaryCodecRoundTrip/
content/testdata/fuzz/FuzzDecode/
crypto/testdata/fuzz/FuzzDecryptMalformed/
crypto/testdata/fuzz/FuzzSealOpenRoundTrip/
notify/webhook/testdata/fuzz/FuzzVerifySignature/
notify/webhook/testdata/fuzz/FuzzVerifySignatureFreshness/
rules/testdata/fuzz/FuzzCompile/
```

Each file holds one seed entry, e.g.:

```
go test fuzz v1
string("application/json")
string("{\"sensor\":\"temp\",\"value\":21.5}")
```

`f.Add(...)` calls in the harness provide additional in-code seeds. Interesting
inputs the fuzzer discovers are written to a machine-local cache
(`$GOCACHE/fuzz`), not to `testdata`.

## Replaying a crash

When a fuzz run fails, Go writes the failing input to
`testdata/fuzz/<Target>/<hash>` and prints a replay command. Re-run just that
input as a unit test:

```bash
go test -run='FuzzVerifySignature/<hash>' ./notify/webhook
```

The committed seed corpus also runs automatically on every `go test ./...`, so a
regression captured as a corpus file keeps failing until it is fixed.

## Issue uncovered by fuzzing (fixed)

`FuzzVerifySignature` with an unconstrained signature header surfaced a real
verifier defect in `notify/webhook/router.go`:

- **`VerifySignature` did not validate the `sha256=` prefix.** It compared
  `sigHeader[len(prefix):]` against the expected HMAC without first checking
  that the stripped bytes actually equalled `sha256=`, so a header carrying the
  correct HMAC digest behind **any** 7-byte prefix (e.g. `0000000<digest>` or
  `sha512=<sha256-digest>`) verified as valid.

`VerifySignature` now rejects any header that does not carry the `sha256=`
prefix. `TestVerifySignaturePrefixBypass` in `notify/webhook/fuzz_test.go` is a
regression test for this, and `FuzzVerifySignature` explores the full
unconstrained header space (including the prefix logic).
