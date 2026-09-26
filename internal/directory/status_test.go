package directory

import (
	"errors"
	"net/http/httptest"
	"testing"
)

// docs/06: only the worker's own refusal is one; any other answer from the
// path to it is the worker being unavailable.
func TestStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   func(error) bool
	}{
		{"created", 201, `{"seq":2}`, func(err error) bool { return err == nil }},
		{"the worker's refusal", 403, `{"error":"bad-assertion"}`, func(err error) bool {
			var ref *Refused
			return errors.As(err, &ref) && ref.Status == 403 && ref.Reason == "bad-assertion"
		}},
		{"a full fleet", 413, `{"error":"fleet-full"}`, func(err error) bool { return errors.As(err, new(*Refused)) }},
		{"no such fleet", 404, `{"error":"no-fleet"}`, func(err error) bool { return errors.Is(err, ErrNoFleet) }},
		{"no fleet for the token", 401, `{"error":"unauthorized"}`, func(err error) bool { return errors.Is(err, ErrUnauthorized) }},
		{"a fleet that exists", 409, `{"error":"exists"}`, func(err error) bool { return errors.Is(err, ErrExists) }},
		{"the worker's rate", 429, `{"error":"rate-limited"}`, func(err error) bool { return errors.Is(err, ErrUnavailable) }},
		{"a proxy's page", 403, `<html>blocked</html>`, func(err error) bool { return errors.Is(err, ErrUnavailable) }},
		{"a proxy asking to authenticate", 407, ``, func(err error) bool { return errors.Is(err, ErrUnavailable) }},
		{"a captive portal's 404", 404, `<html>sign in</html>`, func(err error) bool { return errors.Is(err, ErrUnavailable) }},
		{"a reason the worker never gives", 400, `{"error":"denied"}`, func(err error) bool { return errors.Is(err, ErrUnavailable) }},
	} {
		rec := httptest.NewRecorder()
		rec.WriteHeader(tc.status)
		rec.WriteString(tc.body)
		if err := status(rec.Result()); !tc.want(err) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
