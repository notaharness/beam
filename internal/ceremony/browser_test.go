package ceremony

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// docs/10: the one boundary the test authenticator cannot exercise. The real
// page, in Chromium with a virtual authenticator that does PRF
// (worker/test/callback.mjs), creates a passkey and then signs with it; each
// time /cb lands the fragment and the ceremony completes with a result that
// verifies. It needs `npm ci` and `npx playwright install chromium` in
// worker/, and skips without Playwright there.
func TestBrowserCallback(t *testing.T) {
	if _, err := os.Stat("../../worker/node_modules/playwright"); err != nil {
		t.Skip("no Playwright in worker/node_modules")
	}
	t.Setenv("BEAM_TEST_AUTHENTICATOR", "") // the browser answers
	browser := exec.Command("node", "test/callback.mjs")
	browser.Dir, browser.Stderr = "../../worker", os.Stderr
	urls, _ := browser.StdinPipe()
	out, _ := browser.StdoutPipe()
	if err := browser.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	go func() { _ = browser.Wait(); cancel() }() // a browser that dies ends the wait
	defer urls.Close()
	finished := bufio.NewScanner(out)
	run := func(req Request) Result {
		t.Helper()
		c, err := Start(req)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(urls, c.URL)
		r, err := c.Wait(ctx)
		if err != nil {
			t.Fatalf("%s: %v", req.Op, err)
		}
		if !finished.Scan() || finished.Text() != "done" {
			t.Fatalf("%s: the page did not finish", req.Op)
		}
		return r
	}
	challenge := func() []byte {
		b := make([]byte, 32)
		rand.Read(b)
		return b
	}
	c1, c2 := challenge(), challenge()
	cred, err := run(Request{Op: Create, Action: "Create your beam fleet", Label: "laptop", Fingerprint: "b7f3 9a21 0c4e 55d1",
		Challenge: c1, FleetName: "beam"}).Credential(c1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got := run(Request{Op: Get, Action: "Add laptop to your fleet", Label: "laptop", Fingerprint: "b7f3 9a21 0c4e 55d1", Challenge: c2})
	if err := cred.VerifyAssertion(got.Assertion(), c2); err != nil {
		t.Errorf("get: %v", err)
	}
	if _, _, err := got.DirectoryKeys(); err != nil {
		t.Errorf("get: %v", err)
	}
}
