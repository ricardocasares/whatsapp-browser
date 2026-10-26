Copy of github.com/coder/websocket v1.8.15 with a one-line patch in ws_js.go:
the OnError handler calls closeWithInternal on a new goroutine so it cannot
block the JS event loop while a Go-initiated close is waiting for the close
event. Wired in via a replace directive in go.mod.
