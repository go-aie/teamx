//go:build e2e

package room

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestSpaceRuntimeBusinessBrowser(t *testing.T) {
	if os.Getenv("TEAMX_SPACE_IDENTITY_BROWSER") != "1" {
		t.Skip("optional Chromium acceptance of actual SpaceRuntime assembly")
	}
	defaults := testTeamDefinition(t).Runtime
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	endpoint := "http://" + listener.Addr().String()
	r := spaceRuntimeFixtureAt(t, endpoint)
	r.defaults, r.startup.Runtime = defaults, defaults
	r.factories.service = controlledActivationFactory(nil, nil)
	ready, done := make(chan struct{}), make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { done <- r.Serve(ctx, listener, func(any) error { close(ready); return nil }, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("runtime did not settle")
		}
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("runtime not ready")
	}
	script := `const {chromium}=require('playwright'),assert=require('node:assert/strict');
 (async()=>{const b=await chromium.launch({headless:true});try{
 const page=await b.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));page.on('requestfailed',req=>{if(req.failure()?.errorText!=='net::ERR_ABORTED')errors.push(req.url()+':'+req.failure()?.errorText)});
 await page.goto(process.argv[1]);await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 const identity=await page.evaluate(()=>fetch('/v1/access/session').then(r=>r.json()));
 assert.equal(identity.login_enabled,false);
 const rooms=await page.evaluate(id=>fetch('/v1/spaces/'+id+'/rooms').then(r=>r.json()),identity.space_id);
 assert.equal(rooms.default_assistant.status,'completed');
 assert.equal(await page.evaluate(()=>fetch('/v1/spaces/foreign/rooms').then(r=>r.status)),403);
 await page.reload();await page.locator('#room-list [data-default-assistant="true"]').waitFor();
 assert.deepEqual(await page.evaluate(()=>fetch('/v1/access/session').then(r=>r.json())),identity);
 assert.deepEqual(errors,[]);
 }finally{await b.close()}})().catch(e=>{console.error(e);process.exit(1)});`
	command := exec.CommandContext(t.Context(), "node", "-e", script, endpoint)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Chromium runtime: %v\n%s", err, output)
	}
}
