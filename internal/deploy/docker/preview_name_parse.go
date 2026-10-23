package docker

import (
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// ParsePreviewChangeID is the inverse of previewChangeName: strict — the whole suffix must be the id, so
// apptown-preview-change-7-old never reads as 7.
func ParsePreviewChangeID(name string) (ids.ID, bool) {
	label, ok := previewLabel(name)
	if !ok {
		return ids.Nil, false
	}
	rest, ok := strings.CutPrefix(label, "change-")
	if !ok {
		return ids.Nil, false
	}
	id, err := ids.Parse(rest)
	if err != nil || id.IsZero() {
		return ids.Nil, false
	}
	return id, true
}

// ChangeBuildImages selects every change preview's build images (image ls
// reference filters, ORed): its app's and its parts'.
func ChangeBuildImages() []string {
	return []string{string(Current) + "preview-build-" + string(Current) + "preview-change-*"}
}

// ChangeOfBuildImage is the change whose preview a build image is, by its
// repository (BuildTag of the preview, or of a part: <tag>-<part>). The
// change's id is the 36 characters after the prefix.
func ChangeOfBuildImage(repository string) (ids.ID, bool) {
	rest, ok := strings.CutPrefix(repository, string(Current)+"preview-build-"+string(Current)+"preview-change-")
	if !ok || len(rest) < 36 || (len(rest) > 36 && rest[36] != '-') {
		return ids.Nil, false
	}
	return ParsePreviewChangeID(string(Current) + "preview-change-" + rest[:36])
}
