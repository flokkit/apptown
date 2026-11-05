package httpapi

import (
	"io"
	"net/http"

	"github.com/flokkit/apptown/internal/console"
)

// multipartFileCommand admits one bounded file to execute after the whole
// multipart request has been decoded. Callers check authority before decoding.
func multipartFileCommand(w http.ResponseWriter, r *http.Request, execute func(console.File)) {
	r.Body = http.MaxBytesReader(w, r.Body, console.MaxUploadBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		WriteError(w, 400, "Choose a file to attach.")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		WriteError(w, 400, "Choose a file to attach.")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(part, console.MaxUploadBytes+1))
	if len(raw) > console.MaxUploadBytes {
		WriteError(w, 413, "File too big — 25 MB is the cap.")
		return
	}
	if err != nil {
		WriteError(w, 400, "Could not read the file.")
		return
	}
	if _, err := reader.NextPart(); err != io.EOF {
		WriteError(w, 400, "Upload one file at a time.")
		return
	}
	execute(console.File{Name: part.FileName(), Body: raw})
}
