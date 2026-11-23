package update

import (
	"errors"
	"fmt"

	"github.com/20Youssef10/golab/internal/logx"
)

// Sentinel errors returned by this package. Callers should use errors.Is /
// errors.As rather than matching strings (docs/03 §5).
var (
	ErrDisabled          = errors.New("update checks are disabled")
	ErrNoRelease         = errors.New("no suitable release found")
	ErrDowngrade         = errors.New("update would downgrade the installed version")
	ErrInvalidVersion    = errors.New("invalid version")
	ErrMissingChecksum   = errors.New("release checksum is missing")
	ErrChecksumMismatch  = errors.New("release checksum does not match")
	ErrMissingSignature  = errors.New("release signature is missing")
	ErrInvalidSignature  = errors.New("release signature verification failed")
	ErrMissingSBOM       = errors.New("release SBOM attestation is missing")
	ErrTrustedRoot       = errors.New("sigstore public good trusted root is unavailable")
	ErrIdentityMismatch  = errors.New("release signer identity does not match the pinned identity")
	ErrTruncatedDownload = errors.New("truncated download")
	ErrBadRelease        = errors.New("invalid release metadata")
	ErrTooLarge          = errors.New("download exceeds size limit")
)

// ChecksumError is the typed error for a checksum mismatch. It unwraps to
// ErrChecksumMismatch so errors.Is keeps working.
type ChecksumError struct {
	Got  string
	Want string
}

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("%s: got %s, want %s", ErrChecksumMismatch, e.Got, e.Want)
}

func (e *ChecksumError) Unwrap() error { return ErrChecksumMismatch }

// HTTPError is the typed error for a non-2xx response from the release source.
// It carries only the status code and a public URL — no credentials.
type HTTPError struct {
	StatusCode int
	URL        string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("update request to %s returned HTTP %d", logx.Redact(e.URL), e.StatusCode)
}

// redactErr keeps the original cause (errors.Is/As still work) while removing
// credentials that net/http may embed in a url.Error's request URL.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

func redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := logx.Redact(err.Error())
	if msg == err.Error() {
		return err
	}
	return &redactedError{msg: msg, cause: err}
}
