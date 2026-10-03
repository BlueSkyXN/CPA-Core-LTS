package pluginhost

import (
	"context"
	"testing"
)

func TestHostHTTPDetachPreservesOnlyOwnedCallbackOperations(t *testing.T) {
	host := New()
	instance := &hostCallbackInstance{}
	caller := withHostCallbackIdentity(context.Background(), "plugin", instance)
	callbackID, closeCallback := host.openCallbackContextForPluginInstance(caller, "plugin", instance)
	defer closeCallback()
	owned, err := host.acquireHostHTTPOperation(caller, callbackID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer owned.finish()
	background, err := host.acquireHostHTTPOperation(caller, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer background.finish()

	host.httpOperations.detachInstance("plugin", instance)
	if owned.ctx.Err() != nil {
		t.Fatal("detach canceled the active RPC's HTTP operation")
	}
	if background.ctx.Err() != context.Canceled {
		t.Fatal("detach retained an unowned background operation")
	}
	if _, err = host.acquireHostHTTPOperation(caller, "", ""); err == nil {
		t.Fatal("detached instance started new background work")
	}
	continuation, err := host.acquireHostHTTPOperation(caller, callbackID, "")
	if err != nil {
		t.Fatalf("active RPC cannot continue its HTTP work: %v", err)
	}
	defer continuation.finish()

	replacement := &hostCallbackInstance{}
	replacementCaller := withHostCallbackIdentity(context.Background(), "plugin", replacement)
	if _, err = host.acquireHostHTTPOperation(replacementCaller, callbackID, ""); err == nil {
		t.Fatal("replacement instance inherited an old callback scope")
	}
	newOperation, err := host.acquireHostHTTPOperation(replacementCaller, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer newOperation.finish()
	host.httpOperations.detachInstance("plugin", instance)
	if newOperation.ctx.Err() != nil {
		t.Fatal("old instance detach canceled replacement work")
	}

	closeCallback()
	if owned.ctx.Err() != context.Canceled || continuation.ctx.Err() != context.Canceled {
		t.Fatal("callback scope close did not release its HTTP operations")
	}
	if _, err = host.acquireHostHTTPOperation(caller, callbackID, ""); err == nil {
		t.Fatal("closed callback scope accepted late work")
	}
}
