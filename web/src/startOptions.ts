// The curated ways to start work, by intent — one vocabulary for the
// New-change dialog and the change page's actions. Only workflows actually
// installed in a project are offered. (The word is WORKFLOW everywhere;
// "lane" is retired — naming-principles: one concept, one word.)
export type StartOption = { wf: string; label: string; hint: string; bodyHint?: string }

export const START_OPTIONS: StartOption[] = [
  { wf: 'fix-bug', label: 'Fix a bug', hint: 'reproduce first, then fix', bodyHint: 'Paste the error / describe the bug. The agent must reproduce it before fixing.' },
  { wf: 'quick-change', label: 'Quick change', hint: 'a small change with one approval' },
  { wf: 'plan-execute', label: 'Plan & execute', hint: 'broader scope · you approve the plan first' },
  { wf: 'implement', label: 'Build', hint: 'a new app or a larger feature, from this description' },
  { wf: 'dependency-uplift', label: 'Dependency uplift', hint: 'bring dependencies current' },
  { wf: 'adopt-integration', label: 'Adopt an integration', hint: 'connect the app to a newly attached integration' },
  { wf: 'retire-integration', label: 'Retire an integration', hint: 'take a retired integration out of the code' },
]
