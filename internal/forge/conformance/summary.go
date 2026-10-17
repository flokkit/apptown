package conformance

// Summary is the fleet-level reading of a conformance lens. Counts are facts,
// not arbitrary JSON entries supplied by a transport or a persistence caller.
type Summary struct {
	Projects   int              `json:"projects"`
	ByStanding map[Standing]int `json:"byStanding"`
	Findings   int              `json:"findings"`
	Flagged    int              `json:"flagged"`
	High       int              `json:"high"`
	Checks     int              `json:"checks"`
}

// SummarizeFleet describes the same project readings shown in the fleet view.
func SummarizeFleet(projects []ProjectStanding, checks int) Summary {
	summary := Summary{Projects: len(projects), Checks: checks, ByStanding: map[Standing]int{}}
	for _, p := range projects {
		summary.ByStanding[p.Standing]++
		summary.Findings += p.Lanes.Findings
		summary.Flagged += p.Lanes.Flagged
		summary.High += p.High
	}
	return summary
}
