package console

// Own external tests exercise private behavior without expanding production APIs.
const Attached = attached
const Detached = detached
const Stopped = stopped
const Working = working

var ErrAsleep = errAsleep
var ErrNoLocalChats = errNoLocalChats
var ServiceRecordsMatching = (*Service).recordsMatching
