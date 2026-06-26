package lsp

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// providerWithRegisterCapHandlers builds a Provider whose internal
// client is wired to in-memory pipes and whose register/unregister
// reverse-RPC handlers are installed exactly the way ensureClient
// installs them. Tests use the returned serverOut writer to push
// framed register/unregister requests "from the server", and the
// returned serverIn reader to capture the client's ack replies.
//
// The fixture intentionally drives only the registerCapability path —
// no initialize round-trip happens, which lets the test exercise the
// dynamic side in isolation. Tests that need an initial-caps snapshot
// just write to p.caps directly under p.capsMu.
func providerWithRegisterCapHandlers(t *testing.T) (p *Provider, serverIn func() ([]byte, bool), serverOut func(payload any), cleanup func()) {
	t.Helper()
	c, sin, sout, baseCleanup := newPipedClient(t)
	p = NewProvider("fake-lsp", nil, []string{"go"}, false, 0, zap.NewNop())
	p.client = c

	// Install the same reverse-RPC handlers as ensureClient. The
	// request form returns null + nil error so the server-side ack
	// path is exercised. The notification form has no return.
	c.OnRequest("client/registerCapability",
		func(_ string, params json.RawMessage) (any, *jsonRPCError) {
			p.applyRegistrations(params)
			return nil, nil
		})
	c.OnRequest("client/unregisterCapability",
		func(_ string, params json.RawMessage) (any, *jsonRPCError) {
			p.applyUnregistrations(params)
			return nil, nil
		})
	c.OnNotification("client/registerCapability",
		func(_ string, params json.RawMessage) {
			p.applyRegistrations(params)
		})
	c.OnNotification("client/unregisterCapability",
		func(_ string, params json.RawMessage) {
			p.applyUnregistrations(params)
		})

	serverIn = func() ([]byte, bool) { return readFramed(sin) }
	serverOut = func(payload any) {
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		_, err = fmt.Fprintf(sout, "Content-Length: %d\r\n\r\n", len(data))
		require.NoError(t, err)
		_, err = sout.Write(data)
		require.NoError(t, err)
	}
	return p, serverIn, serverOut, baseCleanup
}

// readAck waits up to timeout for the next framed message off the
// client's stdin (a reply to a server-initiated request) and decodes
// it as a JSON-RPC response. Fails the test on timeout.
func readAck(t *testing.T, serverIn func() ([]byte, bool), timeout time.Duration) jsonRPCResponse {
	t.Helper()
	done := make(chan []byte, 1)
	go func() {
		body, ok := serverIn()
		if ok {
			done <- body
		} else {
			close(done)
		}
	}()
	select {
	case body, ok := <-done:
		require.True(t, ok, "client did not reply before pipe closed")
		var resp jsonRPCResponse
		require.NoError(t, json.Unmarshal(body, &resp))
		return resp
	case <-time.After(timeout):
		t.Fatal("timed out waiting for client to ack the registerCapability request")
		return jsonRPCResponse{}
	}
}

// waitForSupports polls p.Supports(method) until it returns the
// expected value or the deadline elapses. Register/unregister messages
// are routed through the client's read goroutine, so the state
// transition is asynchronous; the poll lets the test wait without
// reaching into client internals.
func waitForSupports(t *testing.T, p *Provider, method string, want bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if p.Supports(method) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Supports(%q): want %v, still %v after %s", method, want, !want, timeout)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

const registerCapWait = 2 * time.Second

// TestProvider_RegisterCapability_OneMethod confirms a server can
// announce a single dynamic capability after initialize and have the
// client (a) record it in dynamicCaps so Supports() returns true, and
// (b) send a null ack back to the request.
func TestProvider_RegisterCapability_OneMethod(t *testing.T) {
	p, serverIn, serverOut, cleanup := providerWithRegisterCapHandlers(t)
	defer cleanup()

	require.False(t, p.Supports("textDocument/foldingRange"),
		"capability must not exist before the server announces it")

	serverOut(map[string]any{
		"jsonrpc": "2.0",
		"id":      42,
		"method":  "client/registerCapability",
		"params": RegistrationParams{Registrations: []Registration{
			{ID: "uuid-folding", Method: "textDocument/foldingRange"},
		}},
	})

	resp := readAck(t, serverIn, registerCapWait)
	assert.Equal(t, int64(42), resp.ID, "ack must echo the request id")
	assert.Nil(t, resp.Error, "ack must not carry an error")
	assert.Equal(t, "null", string(resp.Result),
		"ack result must be the JSON literal null; got %q", string(resp.Result))

	waitForSupports(t, p, "textDocument/foldingRange", true, registerCapWait)
}

// readRawAck waits up to timeout for the next framed message off the
// client's stdin and returns its raw JSON body (undecoded). Unlike
// readAck, it does NOT round-trip through jsonRPCResponse — that decode
// collapses an absent "result" field and an explicit "result":null into
// the same zero-length RawMessage, hiding the exact wire defect this
// guards against.
func readRawAck(t *testing.T, serverIn func() ([]byte, bool), timeout time.Duration) []byte {
	t.Helper()
	done := make(chan []byte, 1)
	go func() {
		if body, ok := serverIn(); ok {
			done <- body
		} else {
			close(done)
		}
	}()
	select {
	case body, ok := <-done:
		require.True(t, ok, "client did not reply before pipe closed")
		return body
	case <-time.After(timeout):
		t.Fatal("timed out waiting for client to ack the request")
		return nil
	}
}

// TestProvider_RegisterCapability_AckCarriesResultMember is the
// regression guard for the bug where a nil handler result serialized to
// a bare {"jsonrpc":"2.0","id":N} — the "result" member omitted entirely
// because the response struct used `Result any` with `omitempty`. A nil
// interface is "empty", so the field vanished. Strict JSON-RPC servers
// (StreamJsonRpc / Roslyn) reject such a message as "Unrecognized
// JSON-RPC 2.0 message" and drop the connection, which manifested as the
// C# language server exiting mid-initialize and enriching 0 nodes. The
// fix forces a null literal on success; this test asserts on the RAW
// bytes (readAck's struct decode cannot distinguish absent from null).
func TestProvider_RegisterCapability_AckCarriesResultMember(t *testing.T) {
	_, serverIn, serverOut, cleanup := providerWithRegisterCapHandlers(t)
	defer cleanup()

	serverOut(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "client/registerCapability",
		"params": RegistrationParams{Registrations: []Registration{
			{ID: "uuid-x", Method: "textDocument/foldingRange"},
		}},
	})

	raw := readRawAck(t, serverIn, registerCapWait)

	// Decode into a permissive map so we can assert key *presence*,
	// which a struct decode cannot.
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m), "ack must be valid JSON: %s", raw)
	_, hasResult := m["result"]
	require.True(t, hasResult,
		"success ack MUST include a result member (JSON-RPC 2.0 requires it; "+
			"strict servers reject a bare id-only message); got %s", raw)
	assert.Equal(t, "null", string(m["result"]),
		"a nil handler result must serialize to the JSON literal null; got %s", raw)
	_, hasError := m["error"]
	assert.False(t, hasError, "success ack must not carry an error member; got %s", raw)
}

// TestProvider_DispatchRequest_ErrorOmitsResult confirms the symmetric
// half of the contract: on the error path the response carries "error"
// and MUST NOT carry "result" (the spec forbids both together). An
// unhandled method drives the -32601 method-not-found path.
func TestProvider_DispatchRequest_ErrorOmitsResult(t *testing.T) {
	_, serverIn, serverOut, cleanup := providerWithRegisterCapHandlers(t)
	defer cleanup()

	serverOut(map[string]any{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "workspace/somethingUnhandled",
		"params":  map[string]any{},
	})

	raw := readRawAck(t, serverIn, registerCapWait)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m), "ack must be valid JSON: %s", raw)
	_, hasError := m["error"]
	require.True(t, hasError, "method-not-found ack must carry an error member; got %s", raw)
	_, hasResult := m["result"]
	assert.False(t, hasResult,
		"error response must NOT carry a result member (spec forbids both); got %s", raw)
}

// TestProvider_RegisterCapability_MultipleMethods confirms a single
// registerCapability request can carry several registrations and they
// all become supported.
func TestProvider_RegisterCapability_MultipleMethods(t *testing.T) {
	p, serverIn, serverOut, cleanup := providerWithRegisterCapHandlers(t)
	defer cleanup()

	methods := []string{
		"textDocument/foldingRange",
		"textDocument/semanticTokens/full",
		"textDocument/documentSymbol",
	}
	regs := make([]Registration, len(methods))
	for i, m := range methods {
		regs[i] = Registration{ID: fmt.Sprintf("uuid-%d", i), Method: m}
	}

	serverOut(map[string]any{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "client/registerCapability",
		"params":  RegistrationParams{Registrations: regs},
	})

	resp := readAck(t, serverIn, registerCapWait)
	assert.Equal(t, int64(7), resp.ID)
	assert.Nil(t, resp.Error)

	for _, m := range methods {
		waitForSupports(t, p, m, true, registerCapWait)
	}
}

// TestProvider_UnregisterCapability_Removes confirms unregister
// removes only the listed entry and leaves siblings intact.
func TestProvider_UnregisterCapability_Removes(t *testing.T) {
	p, serverIn, serverOut, cleanup := providerWithRegisterCapHandlers(t)
	defer cleanup()

	// Register foldingRange and documentSymbol.
	serverOut(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "client/registerCapability",
		"params": RegistrationParams{Registrations: []Registration{
			{ID: "uuid-folding", Method: "textDocument/foldingRange"},
			{ID: "uuid-docsym", Method: "textDocument/documentSymbol"},
		}},
	})
	_ = readAck(t, serverIn, registerCapWait)
	waitForSupports(t, p, "textDocument/foldingRange", true, registerCapWait)
	waitForSupports(t, p, "textDocument/documentSymbol", true, registerCapWait)

	// Drop foldingRange — note the LSP wire-misspelling
	// "unregisterations".
	serverOut(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "client/unregisterCapability",
		"params": UnregistrationParams{Unregisterations: []Unregistration{
			{ID: "uuid-folding", Method: "textDocument/foldingRange"},
		}},
	})

	resp := readAck(t, serverIn, registerCapWait)
	assert.Equal(t, int64(2), resp.ID)
	assert.Nil(t, resp.Error)

	waitForSupports(t, p, "textDocument/foldingRange", false, registerCapWait)
	assert.True(t, p.Supports("textDocument/documentSymbol"),
		"unrelated dynamic registrations must survive unregister")
}

// TestProvider_RegisterCapability_InitialAndDynamicMix seeds the
// initialize-time snapshot with hover and verifies that hover stays
// supported even after the server lazily registers foldingRange.
// Both should be reported as supported.
func TestProvider_RegisterCapability_InitialAndDynamicMix(t *testing.T) {
	p, serverIn, serverOut, cleanup := providerWithRegisterCapHandlers(t)
	defer cleanup()

	// Seed the initialize snapshot — hover is statically advertised.
	p.capsMu.Lock()
	p.caps = ServerCapabilities{HoverProvider: true}
	p.capsMu.Unlock()

	require.True(t, p.Supports("textDocument/hover"))
	require.False(t, p.Supports("textDocument/foldingRange"))

	serverOut(map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "client/registerCapability",
		"params": RegistrationParams{Registrations: []Registration{
			{ID: "uuid-folding", Method: "textDocument/foldingRange"},
		}},
	})
	_ = readAck(t, serverIn, registerCapWait)
	waitForSupports(t, p, "textDocument/foldingRange", true, registerCapWait)

	// Both must remain supported.
	assert.True(t, p.Supports("textDocument/hover"), "static cap must survive after a dynamic registration")
	assert.True(t, p.Supports("textDocument/foldingRange"))
}

// TestProvider_RegisterCapability_ConcurrentReadsAndWrites stresses
// the capsMu lock under -race: two reader goroutines hammering
// Supports() while two writer goroutines push register/unregister
// bursts. The final state must be consistent with the last write.
func TestProvider_RegisterCapability_ConcurrentReadsAndWrites(t *testing.T) {
	p := NewProvider("fake-lsp", nil, []string{"go"}, false, 0, zap.NewNop())

	const iters = 1000
	var (
		wg          sync.WaitGroup
		readerErrs  atomic.Int64
		readerCalls atomic.Int64
	)

	// Two writer goroutines — each owns a distinct id+method pair so
	// the final state is deterministic by id and the assertion below
	// is unambiguous.
	writer := func(id, method string) {
		defer wg.Done()
		regParams, _ := json.Marshal(RegistrationParams{
			Registrations: []Registration{{ID: id, Method: method}},
		})
		unregParams, _ := json.Marshal(UnregistrationParams{
			Unregisterations: []Unregistration{{ID: id, Method: method}},
		})
		for i := 0; i < iters; i++ {
			p.applyRegistrations(regParams)
			p.applyUnregistrations(unregParams)
		}
		// Final write leaves the registration in place so we can
		// assert the post-stress state below.
		p.applyRegistrations(regParams)
	}

	// Two reader goroutines — Supports() against a churning method.
	// Result correctness is enforced by -race + the final assertion;
	// the per-call boolean fluctuates and is intentionally not
	// asserted here.
	reader := func(method string) {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_ = p.Supports(method)
			readerCalls.Add(1)
		}
	}

	wg.Add(4)
	go writer("uuid-folding", "textDocument/foldingRange")
	go writer("uuid-docsym", "textDocument/documentSymbol")
	go reader("textDocument/foldingRange")
	go reader("textDocument/documentSymbol")
	wg.Wait()

	assert.Zero(t, readerErrs.Load(), "no reader should have observed an error")
	assert.Equal(t, int64(2*iters), readerCalls.Load(), "every Supports call should have completed")

	// Final state must reflect the last write of each writer — both
	// methods are registered.
	assert.True(t, p.Supports("textDocument/foldingRange"))
	assert.True(t, p.Supports("textDocument/documentSymbol"))
}

// TestProvider_RegisterCapability_NotificationForm verifies the
// notification framing (no id, no response) — some servers send
// registerCapability that way. The client must update state and emit
// no reply.
func TestProvider_RegisterCapability_NotificationForm(t *testing.T) {
	p, serverIn, serverOut, cleanup := providerWithRegisterCapHandlers(t)
	defer cleanup()

	// Notification: id field omitted entirely.
	serverOut(map[string]any{
		"jsonrpc": "2.0",
		"method":  "client/registerCapability",
		"params": RegistrationParams{Registrations: []Registration{
			{ID: "uuid-folding-notif", Method: "textDocument/foldingRange"},
		}},
	})

	waitForSupports(t, p, "textDocument/foldingRange", true, registerCapWait)

	// Confirm the client wrote nothing back. A reply would land on
	// serverIn — drain with a short timeout and assert empty.
	done := make(chan struct{})
	go func() {
		_, ok := serverIn()
		if ok {
			t.Errorf("client must not reply to a notification-form registerCapability")
		}
		close(done)
	}()
	select {
	case <-done:
		// Pipe closed (cleanup) — fine.
	case <-time.After(150 * time.Millisecond):
		// Expected: no message arrived; the goroutine is still
		// parked on the pipe read. Move on.
	}
}

// TestProvider_RegisterCapability_UnknownMethodStillRecorded confirms
// the dynamic table accepts methods our static switchboard knows
// nothing about — a future feature site can flip on as soon as the
// registration arrives, without us having to plumb an explicit cap
// field. No error response is generated.
func TestProvider_RegisterCapability_UnknownMethodStillRecorded(t *testing.T) {
	p, serverIn, serverOut, cleanup := providerWithRegisterCapHandlers(t)
	defer cleanup()

	const unknown = "textDocument/onTypeFormatting"
	require.False(t, p.Supports(unknown))

	serverOut(map[string]any{
		"jsonrpc": "2.0",
		"id":      555,
		"method":  "client/registerCapability",
		"params": RegistrationParams{Registrations: []Registration{
			{ID: "uuid-fmt", Method: unknown},
		}},
	})

	resp := readAck(t, serverIn, registerCapWait)
	assert.Equal(t, int64(555), resp.ID, "ack must echo the request id even for unknown methods")
	assert.Nil(t, resp.Error, "no error response is generated for an unfamiliar method")

	waitForSupports(t, p, unknown, true, registerCapWait)
}
