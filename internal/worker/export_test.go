package worker

import "net/http"

// FailAfterRename makes every durable write of name fail right after its
// rename has made the new file visible, as a failed folder sync would, until
// the returned func restores it.
func FailAfterRename(name string, err error) func() {
	previous := afterRename
	afterRename = func(_, n string) error {
		if n == name {
			return err
		}
		return nil
	}
	return func() { afterRename = previous }
}

// AbandonAt makes the worker abandon the request handling key's task at
// point (server.go, abandonAt), as a worker stopped there would: nothing
// after it in that request runs. The returned func restores it.
func AbandonAt(point, key string) func() {
	previous := abandonAt
	abandonAt = func(p, k string) {
		if p == point && k == key {
			panic(http.ErrAbortHandler)
		}
	}
	return func() { abandonAt = previous }
}

const PreviewSeqHeader = previewSeqHeader
const PreviewSessionHeader = previewSessionHeader
const ProtocolHeader = protocolHeader
const RecordAsOfFile = recordAsOfFile

var ErrNoSession = errNoSession
var ErrNotConfirmed = errNotConfirmed
var ErrSessionBusy = errRemoteSessionBusy
var ErrSessionRemoved = errRemoteSessionRemoved
var SessionClientEnsureBlob = (SessionClient).ensureBlob
var SessionClientPutFiles = (SessionClient).putFiles
var SessionClientPutSource = (SessionClient).putSource
var SessionClientRecord = (SessionClient).record
var SessionClientStart = (SessionClient).start
var SessionClientTerminal = (SessionClient).terminal
