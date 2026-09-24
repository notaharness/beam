package cli

import (
	"errors"
	"fmt"

	"github.com/notaharness/beam/internal/control"
)

// explanations are the ceremony commands' error tokens in words (docs/07,
// Ceremony errors).
var explanations = map[string]string{
	"prf-unsupported":       "The selected passkey did not provide WebAuthn PRF. beam needs this extension to derive the encrypted fleet directory key. Browser, operating system and passkey provider must all support it.",
	"ceremony-cancelled":    "Passkey request cancelled. No further approval is pending for this request.",
	"ceremony-timeout":      "This passkey request expired after five minutes. Start again to get a new link and QR code.",
	"ceremony-state":        "beam could not use this ceremony result. The request may be stale, already consumed, or answered with data it cannot decrypt. Check this machine’s fleet status (beam status), then start again with a fresh link.",
	"bad-assertion":         "The passkey request failed or its answer could not be verified. Check the browser’s message, then start again.",
	"directory-unavailable": "Cannot read the fleet directory. Check the connection to beam.n10.is and try joining again.",
	"wrong-passkey":         "This passkey does not unlock the expected fleet. Choose the original fleet passkey using a compatible browser and provider.",
	"busy":                  "Another passkey request is already running. Finish or cancel it where you started it, then try again.",
	"already-enrolled":      "This machine already belongs to a fleet. Run beam status to inspect it.",
	"not-enrolled":          "This machine is not in a fleet. Create or join a fleet first (beam init or beam join).",
	"revoked-peer":          "This machine identity has been revoked. Resetting its fleet will not make that identity eligible to rejoin.",
	"unknown-peer":          "This machine is no longer in the local peer list. Check beam peers before trying again.",
	"ambiguous-peer":        "More than one machine matches. Select a machine by its fingerprint.",
	"params":                "beam rejected these details. Check the machine and fleet names.",
	"bad-entry":             "beam rejected an invalid membership record. Check the details before trying again.",
	"storage-failure":       "beam could not save fleet data. Check available disk space and permissions, then retry.",
	"internal":              "beam could not complete this request. Check the details and this machine’s fleet status before retrying.",
}

const (
	lost  = "The connection to beam was interrupted. Check beam status before retrying; the request may have completed."
	same  = "Use the same fleet passkey; a new passkey creates a different fleet."
	saved = "A passkey may have been saved, but fleet creation is not complete."
	// needs is what the page and the passkey need, as vendors document it.
	needs = `beam needs WebAuthn PRF from the browser, the operating system and the passkey provider together, and X25519 in Web Crypto to seal the page’s answer. What vendors document, not tested end to end:
  Safari 18.4+: PRF and X25519; Safari 18.0 to 18.3 has PRF without X25519
  Apple Passwords: iOS and iPadOS 18.4+ with Safari 18.4+; macOS Safari 18.4+ also needs a compatible provider (unverified: the oldest macOS and provider pairing)
  Chrome 133+: X25519; PRF depends on the provider (unverified: Chrome’s first PRF release, Google Password Manager)
  Firefox 139+ on desktop: PRF and X25519; Firefox 130 alone does not promise PRF (unverified: Android, iOS)
  1Password for iOS 8.10.74+: provider PRF on iOS 18; the page also needs iOS 18.4+ for X25519
  1Password browser extension and Android: PRF since betas 2.26.1 and 8.10.38 (unverified: stable versions)
  security keys: WebAuthn PRF, not hmac-secret alone (unverified: firmware and OS minimums)
  unverified: Windows Hello, Bitwarden, Proton Pass, other providers, Edge, Samsung Internet`
)

// interrupted is a ceremony's wait that lost its connection to the daemon.
type interrupted struct{ error }

func (i interrupted) Unwrap() error { return i.error }

// explain is err, which ceremony command op returned, in words, or "" for an
// error that is neither a token nor the connection lost.
func explain(op string, err error) string {
	var oe *control.OpError
	if !errors.As(err, &oe) {
		if errors.As(err, new(interrupted)) {
			return lost
		}
		return ""
	}
	s, ok := explanations[oe.Code]
	if !ok {
		s = explanations["internal"]
	}
	if oe.Code == "prf-unsupported" {
		next := same
		if op == "init" {
			next = saved
		}
		s += "\n" + next + "\n" + needs
	}
	return s
}

// failCeremony is fail for ceremony command op: the token first, then what
// it means.
func (e *env) failCeremony(op string, err error) int {
	code := e.fail(err)
	if s := explain(op, err); s != "" {
		fmt.Fprintln(e.stderr, s)
	}
	return code
}
