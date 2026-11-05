package check

// AgentEvidence identifies one isolated check session. Its transcript and tool
// observations are kept under Session. No complete list of file reads is claimed:
// recursive searches and scripts may read files without naming them in events.
type AgentEvidence struct {
	AppTownFault      bool   `json:"appTownFault,omitempty"`
	SourceReadFailure bool   `json:"sourceReadFailure,omitempty"`
	Session           string `json:"session"`
	Commit            string `json:"commit"`
	Tree              string `json:"tree"`
	DiffHash          string `json:"diffHash,omitempty"`
	RubricHash        string `json:"rubricHash"`
	RubricIdentity    string `json:"rubricIdentity"`
	TemplateHash      string `json:"templateHash"`
	Runtime           string `json:"runtime"`
	RequestedModel    string `json:"requestedModel"`
	ServedModel       string `json:"servedModel"`
	TokensIn          int64  `json:"tokensIn"`
	TokensOut         int64  `json:"tokensOut"`
	DurationMs        int64  `json:"durationMs"`
	UsageIncomplete   string `json:"usageIncomplete,omitempty"`
	Reused            bool   `json:"reused,omitempty"`
}
