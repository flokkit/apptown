---
name: lang/go/errors-are-values
tags: lang,go
---
Go error handling:

- Wrap with context: fmt.Errorf("opening config: %w", err); no bare err returns
  across package boundaries.
- Sentinel errors are package-level vars named ErrX; check with errors.Is.
- Don't log AND return an error — pick one.
- No panics across API boundaries.
