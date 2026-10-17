package forge

import (
	"encoding/json"
	"errors"
	"net/http"

	"flokkit.io/forge/internal/forge/httpapi"

	"flokkit.io/forge/internal/console"

	"github.com/gorilla/websocket"
)

// handleConsolePTY is the terminal websocket: binary frames are raw
// terminal bytes both ways; text frames are control JSON
// ({type:"resize",cols,rows}). GET /console/{sid}/pty. A watcher's
// keystrokes and resizes are dropped by its terminal.
func (a *App) handleConsolePTY(w http.ResponseWriter, r *http.Request) {
	// A forged GET must not start a container or attach a terminal: this
	// browser-only door requires Origin, even for a GET.
	if !a.sameBrowserOrigin(r) {
		writeErr(w, http.StatusForbidden, "open the terminal from Forge")
		return
	}
	s := httpapi.ConsoleFromPath(a.Console, w, r)
	if s == nil {
		return
	}
	// Someone who may act on the chat types into it; anyone else who may
	// look watches its screen (conversation.Chats.Terminal).
	term, err := chatsFor(a).Terminal(r.Context(), s)
	if err != nil {
		httpapi.WriteFaultOr(w, err, 500)
		return
	}
	upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: a.sameBrowserOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = term.Close()
		return
	}
	conn.SetReadLimit(1 << 20)

	// Writer: the terminal's output (recent replayed, then live) to the socket.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				if conn.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		_ = conn.Close()
	}()

	// Reader: keystrokes and resizes.
reading:
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		switch mt {
		case websocket.BinaryMessage:
			if _, err := term.Write(data); err != nil {
				if errors.Is(err, console.ErrWatching) {
					continue // a watcher's keystroke goes nowhere; the screen stays open
				}
				break reading
			}
		case websocket.TextMessage:
			var ctl struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" {
				_ = term.Resize(console.Size{Cols: ctl.Cols, Rows: ctl.Rows})
			}
		}
	}
	_ = term.Close()
	_ = conn.Close()
}
