package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Unknown producer metadata is opaque to the approval model. Keep it private
// to this storage codec as raw JSON, so round changes neither drop it nor round
// large integers. Ordinary approval facts always use ApprovalEvidence.
type approvalDocument struct {
	evidence ApprovalEvidence
	extra    map[string]json.RawMessage
}

func readApprovalDocument(raw []byte) (approvalDocument, error) {
	evidence, err := ReadApprovalEvidence(raw)
	if err != nil {
		return approvalDocument{}, err
	}
	extra := map[string]json.RawMessage{}
	if err := approvalObject(raw, &extra); err != nil {
		return approvalDocument{}, err
	}
	for _, key := range []string{"withdrawal", "previousWithdrawals", "previousDecisions", "decisions", "quorum", "approvals", "means", "approver", "decision", "comment", "at", "lastRejection", "candidate", "evalAuthorised"} {
		delete(extra, key)
	}
	return approvalDocument{evidence, extra}, nil
}
func (d approvalDocument) encode(evidence ApprovalEvidence) (json.RawMessage, error) {
	body, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	if len(d.extra) == 0 {
		return body, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	for k, v := range d.extra {
		fields[k] = v
	}
	return json.Marshal(fields)
}

// Nested vote/feedback metadata remains opaque too. The model reads named facts;
// these codecs retain additional producer fields when a round moves to history.
func mergeApprovalJSON(original json.RawMessage, known []byte) ([]byte, error) {
	if len(original) == 0 {
		return known, nil
	}
	var fields, updated map[string]json.RawMessage
	if err := approvalObject(original, &fields); err != nil {
		return nil, err
	}
	if err := approvalObject(known, &updated); err != nil {
		return nil, err
	}
	for k, v := range updated {
		fields[k] = v
	}
	return json.Marshal(fields)
}
func (v *ApprovalVote) UnmarshalJSON(raw []byte) error {
	type fields ApprovalVote
	var value fields
	if err := approvalObject(raw, &value); err != nil {
		return err
	}
	*v = ApprovalVote(value)
	v.original = bytes.Clone(raw)
	return nil
}
func (v ApprovalVote) MarshalJSON() ([]byte, error) {
	type fields ApprovalVote
	known, err := json.Marshal(fields(v))
	if err != nil {
		return nil, err
	}
	return mergeApprovalJSON(v.original, known)
}
func (v *ApprovalRejection) UnmarshalJSON(raw []byte) error {
	type fields ApprovalRejection
	var value struct {
		fields
		Decision ApprovalDecision `json:"decision"`
	}
	if err := approvalObject(raw, &value); err != nil {
		return err
	}
	if value.Decision == VoteApprove {
		return fmt.Errorf("rejection feedback cannot carry an approving vote")
	}
	*v = ApprovalRejection(value.fields)
	v.original = bytes.Clone(raw)
	return nil
}
func (v ApprovalRejection) MarshalJSON() ([]byte, error) {
	type fields ApprovalRejection
	known, err := json.Marshal(fields(v))
	if err != nil {
		return nil, err
	}
	return mergeApprovalJSON(v.original, known)
}
