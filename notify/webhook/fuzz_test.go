package webhook

import (
	"strconv"
	"testing"
	"time"
)

// FuzzVerifySignature checks VerifySignature against an independently computed
// oracle. The fuzzer controls the entire header value, so both the "sha256="
// prefix validation and the digest comparison are exercised.
//
// With the timestamp check disabled (tolerance == 0), the function must
// return true IFF:
//   - the secret is non-empty, AND
//   - the header equals "sha256=" + sign(secret, timestamp, body).
//
// Any deviation (accepting a wrong digest, accepting a forged/missing prefix,
// accepting an empty secret, or rejecting the correct header) fails the oracle.
func FuzzVerifySignature(f *testing.F) {
	const prefix = "sha256="
	f.Add([]byte("topsecret"), "1700000000", []byte(`{"event":"ping"}`), "sha256=deadbeef")
	f.Add([]byte(""), "1700000000", []byte("body"), "sha256=abc")
	f.Add([]byte("k"), "0", []byte(""), "")
	f.Add([]byte("k"), "notanumber", []byte("x"), "sha512=abc")
	// A seed whose header is the genuinely correct signature.
	f.Add([]byte("secret"), "1700000000", []byte("payload"), prefix+sign([]byte("secret"), "1700000000", []byte("payload")))
	// A seed carrying the correct digest behind a non-"sha256=" prefix — must
	// be rejected (regression seed for the prefix-bypass defect).
	f.Add([]byte("secret"), "1700000000", []byte("payload"), "0000000"+sign([]byte("secret"), "1700000000", []byte("payload")))

	// sigHeader is the full, fuzzer-controlled header value, so the "sha256="
	// prefix logic is exercised (not just the digest comparison).
	f.Fuzz(func(t *testing.T, secret []byte, timestamp string, body []byte, sigHeader string) {
		// Use tolerance 0 so freshness never interferes: the result depends
		// purely on the prefix + secret/signature comparison.
		got := VerifySignature(secret, timestamp, body, sigHeader, 0)

		// A header verifies if and only if the secret is non-empty and the
		// header is exactly "sha256=" + the correct HMAC digest.
		want := false
		if len(secret) > 0 {
			want = sigHeader == prefix+sign(secret, timestamp, body)
		}

		if got != want {
			t.Fatalf("VerifySignature mismatch: secret=%q ts=%q body=%q sig=%q => got=%v want=%v",
				secret, timestamp, body, sigHeader, got, want)
		}
	})
}

// TestVerifySignaturePrefixBypass is a regression test for a verifier defect:
// VerifySignature used to strip the first len("sha256=") bytes of the header
// without confirming they equal "sha256=", so a header carrying the correct
// HMAC digest behind ANY 7-byte prefix verified as valid. VerifySignature now
// validates the prefix, so a forged-prefix header must be rejected.
func TestVerifySignaturePrefixBypass(t *testing.T) {
	secret := []byte("secret")
	ts := "1700000000"
	body := []byte("payload")
	correct := sign(secret, ts, body)

	// "0000000" is a 7-byte non-"sha256=" prefix; the real digest follows.
	forged := "0000000" + correct
	if VerifySignature(secret, ts, body, forged, 0) {
		t.Fatalf("prefix bypass: header %q without 'sha256=' prefix was accepted", forged)
	}
}

// FuzzVerifySignatureFreshness exercises the tolerance branch: a correctly
// signed payload must verify only when its timestamp is within tolerance of
// now. Catches off-by-one / sign errors in the freshness window.
func FuzzVerifySignatureFreshness(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(60))
	f.Add(int64(-60))
	f.Add(int64(600))
	f.Add(int64(-600))

	secret := []byte("freshness-secret")
	body := []byte(`{"k":"v"}`)
	const tolerance = 5 * time.Minute

	f.Fuzz(func(t *testing.T, offsetSec int64) {
		// Bound the offset so arithmetic stays well-defined.
		if offsetSec > 1<<40 || offsetSec < -(1<<40) {
			return
		}
		ts := time.Now().Unix() + offsetSec
		tsStr := strconv.FormatInt(ts, 10)
		sigHeader := "sha256=" + sign(secret, tsStr, body)

		got := VerifySignature(secret, tsStr, body, sigHeader, tolerance)

		// Signature is correct by construction, so the result is governed by
		// freshness alone. The implementation uses abs(now-ts)*sec > tolerance
		// to reject. Recompute conservatively, allowing a 2s slack band around
		// the boundary to absorb the now() drift between the two reads.
		absOff := offsetSec
		if absOff < 0 {
			absOff = -absOff
		}
		switch {
		case absOff <= int64(tolerance/time.Second)-2:
			if !got {
				t.Fatalf("fresh signature rejected: offset=%ds", offsetSec)
			}
		case absOff >= int64(tolerance/time.Second)+2:
			if got {
				t.Fatalf("stale signature accepted: offset=%ds", offsetSec)
			}
		default:
			// Within the boundary slack band: either outcome acceptable.
		}
	})
}
