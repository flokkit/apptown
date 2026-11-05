package workflow

// RequestMessage identifies the exact person words retained from a conversation.
type RequestMessage struct {
	Session string `json:"session"`
	Index   int    `json:"index"`
	At      string `json:"at"`
	Text    string `json:"text"`
}
