package conformance

// Own external tests exercise private behavior without expanding production APIs.
const CheckExcepted = checkExcepted
const CheckFailed = checkFailed
const CheckUnknown = checkUnknown

type FindingSubject = findingSubject

var DefinitionOf = definitionOf
var DispositionActiveAt = (Disposition).activeAt
var ErrDispositionKind = errDispositionKind
var MatchDisposition = matchDisposition
