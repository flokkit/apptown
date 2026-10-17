package httpapi

import "flokkit.io/forge/internal/wiregen"

func init() {
	registerWire(wiregen.Named{Name: "WorkerChatList", Value: WorkerChatList{}})
}
