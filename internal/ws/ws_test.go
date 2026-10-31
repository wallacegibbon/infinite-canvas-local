package ws

// The test client is the one this repository already ships: `internal/comfy` speaks WebSocket to
// ComfyUI, masking its frames and parsing ours. Using it here means the server is exercised by an
// independent implementation rather than by its own reflection.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"comfyui-gateway/internal/comfy"
)

// startEcho runs a server whose socket echoes every message back, upper-cased.
func startEcho(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close()
		for {
			message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteText([]byte(strings.ToUpper(string(message)))); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func dial(t *testing.T, url string) *comfy.WSConn {
	t.Helper()
	conn, err := comfy.DialWS(context.Background(), url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestSocketCarriesMessagesBothWays(t *testing.T) {
	conn := dial(t, startEcho(t))

	if err := conn.WriteText([]byte(`{"prompt":"你好"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	message, err := conn.ReadMessage(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(message) != `{"PROMPT":"你好"}` {
		t.Fatalf("read %q, want the echo", message)
	}
}

// TestSocketCarriesALongMessage crosses the 126-byte length boundary, which is where a frame's length
// stops fitting in the header byte.
func TestSocketCarriesALongMessage(t *testing.T) {
	conn := dial(t, startEcho(t))

	long := strings.Repeat("a", 70000)
	if err := conn.WriteText([]byte(long)); err != nil {
		t.Fatalf("write: %v", err)
	}
	message, err := conn.ReadMessage(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(message) != strings.ToUpper(long) {
		t.Fatalf("read %d bytes, want %d", len(message), len(long))
	}
}

func TestSocketRefusesARequestThatIsNotAHandshake(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Accept(w, r); err == nil {
			t.Error("Accept accepted a plain GET")
		}
	}))
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	response.Body.Close()
}

func TestSocketReportsTheClientGoingAway(t *testing.T) {
	url := startEcho(t)
	conn := dial(t, url)
	if err := conn.WriteText([]byte("hi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.Close()

	// The server side sees the close and stops; a second message from a dead peer is an error rather
	// than a write into nothing.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := conn.ReadMessage(ctx); err == nil {
		t.Fatal("a closed connection still reads")
	}
}
