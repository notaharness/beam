//go:build beamtest

package transport

// devDERP is true in beamtest builds alone, whose DERP map is the dev DERP's
// (docs/10): served over http, its node InsecureForTests.
const devDERP = true
