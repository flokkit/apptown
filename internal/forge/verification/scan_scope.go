package verification

import "flokkit.io/forge/internal/deploy/appcontract"

// imageScanScope says what an image scan of c covers, in its evidence's
// words: the public part whose image it scanned and how that image was
// obtained (note is imageForCommit's note on the path it took: a reused
// release image or a cache hit; "" is a build for the checks), and every
// other part and init step, which the scan does not look at: each ships
// an image (a static part in the forge's file-server image). The release
// resolves its own images, so the receipt never names the release's
// digest.
func imageScanScope(c *appcontract.Contract, note string) (scanned map[string]string, notScanned []string) {
	pub := c.PublicPart()
	how := "built from this commit for the checks; the release builds its own from the same commit"
	switch {
	case pub.Image != "":
		how = "pulled by its reference " + pub.Image
	case note != "":
		how = note
	}
	return map[string]string{"part": pub.Name, "how": how}, otherImages(c)
}

// otherImages is every part but the public one, and every init step.
func otherImages(c *appcontract.Contract) []string {
	var out []string
	for _, p := range c.Parts {
		if !p.Public {
			out = append(out, "part "+p.Name)
		}
	}
	for _, it := range c.Init {
		out = append(out, "init step "+it.Name)
	}
	return out
}
