package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/wiregen"
)

func init() {
	registerWire(wiregen.Named{Name: "RepositoryAttachmentState", Value: scm.AttachmentState{}})
}
