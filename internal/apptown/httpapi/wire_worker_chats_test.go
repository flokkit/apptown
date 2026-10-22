package httpapi

import "github.com/flokkit/apptown/internal/wiregen"

func init() {
	registerWire(wiregen.Named{Name: "WorkerChatList", Value: WorkerChatList{}})
}
