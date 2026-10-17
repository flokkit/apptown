package docker

import (
	"strings"

	"flokkit.io/forge/internal/ids"
)

// ParsePreviewChangeID is the inverse of previewChangeName: strict — the
// whole suffix must be the id, so forge-preview-change-7-old never reads as 7.
func ParsePreviewChangeID(name string) (ids.ID, bool) {
	rest, ok := strings.CutPrefix(name, previewPrefix+"change-")
	if !ok {
		return ids.Nil, false
	}
	id, err := ids.Parse(rest)
	if err != nil || id.IsZero() {
		return ids.Nil, false
	}
	return id, true
}

// ChangeBuildImages selects every change preview's build images (an image
// ls reference filter): its app's and its parts'.
const ChangeBuildImages = buildPrefix + previewPrefix + "change-*"

// ChangeOfBuildImage is the change whose preview a build image is, by its
// repository (BuildTag of the preview, or of a part: <tag>-<part>). The
// change's id is the 36 characters after the prefix.
func ChangeOfBuildImage(repository string) (ids.ID, bool) {
	rest, ok := strings.CutPrefix(repository, buildPrefix+previewPrefix+"change-")
	if !ok || len(rest) < 36 || (len(rest) > 36 && rest[36] != '-') {
		return ids.Nil, false
	}
	return ParsePreviewChangeID(previewPrefix + "change-" + rest[:36])
}
