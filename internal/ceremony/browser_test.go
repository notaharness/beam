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

// docs/10 Browser ceremony: the one boundary the test authenticator cannot
// exercise. The real page, in Chromium with a virtual authenticator that does
// PRF (worker/test/ceremony.mjs), creates a passkey and then signs with it;
// each time it seals its result with Web Crypto and writes it to the slot,
// and the ceremony opens it with crypto/hpke and completes with a result that
// verifies. It needs `npm ci` and `npx playwright install chromium` in
// worker/, and skips without Playwright there.
func TestBrowserCeremony(t *testing.T) {
	if _, err := os.Stat("../../worker/node_modules/playwright"); err != nil {
		t.Skip("no Playwright in worker/node_modules")
	}
	t.Setenv("BEAM_TEST_AUTHENTICATOR", "") // the browser answers
	w := worker(t)
	browser := exec.Command("node", "test/ceremony.mjs", w)
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
		c, err := Start(req, w)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(urls, c.URL)
		r, err := c.Wait(ctx)
		if err != nil {
			t.Fatalf("%s: %v", req.Kind, err)
		}
		if !finished.Scan() || finished.Text() != "done" {
			t.Fatalf("%s: the page did not finish", req.Kind)
		}
		return r
	}
	challenge := func() []byte {
		b := make([]byte, 32)
		rand.Read(b)
		return b
	}
	c1, c2 := challenge(), challenge()
	cred, err := run(Request{Kind: Create, Label: "laptop", PeerID: peerID, Challenge: c1, FleetName: "beam"}).Credential(c1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got := run(Request{Kind: Add, Label: "laptop", PeerID: peerID, Challenge: c2})
	if err := cred.VerifyAssertion(got.Assertion(), c2); err != nil {
		t.Errorf("get: %v", err)
	}
	if _, _, err := got.DirectoryKeys(); err != nil {
		t.Errorf("get: %v", err)
	}
}
