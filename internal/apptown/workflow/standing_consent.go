package workflow

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/flokkit/apptown/internal/apptown/schema"
)

// standingConsent renders the grant and the current publication's illustrative
// values. The grant follows each project's adopted declarations, including when
// a newer pack changes cadence or repair limits without changing purposes.
func standingConsent(categories StandingCategories, covered, library []*schema.Doc) string {
	var declarations []string
	for _, doc := range covered {
		trigger, _ := doc.Standing()
		cadence := schema.CronWords(trigger.CronFor(categories.Dependencies))
		if trigger.Publication {
			cadence = "when a newer pack version is published"
		}
		rounds := doc.RepairRounds()
		for _, name := range doc.Calls() {
			for _, called := range library {
				if called.Metadata.Name == name {
					rounds += called.RepairRounds()
				}
			}
		}
		detail := strings.TrimSuffix(doc.Metadata.Description, ".")
		if detail != "" {
			runes := []rune(detail)
			runes[0] = unicode.ToLower(runes[0])
			detail = string(runes) + "; "
		}
		declarations = append(declarations, fmt.Sprintf("%s %s (%sup to %d repair sessions)", doc.Metadata.DisplayTitle(), cadence, detail, rounds))
	}
	shown := "Under this approval AppTown runs, in each project, the maintenance its adopted packs declare, on their schedule and with their repair limits; a pack version that changes these applies when the project adopts it. The current packs declare " + strings.Join(declarations, " and ") + ". "
	if categories.WaysOfWorking {
		return "Keep packs current. " + shown + "AppTown moves projects to newly published versions of the packs they use. An agent writes any code the change needs. When every required check is met, tests and start-up pass and scans find no new failures, it lands the change and releases it like any janitor change: to deployments that follow main and allow automatic or window releases, unless protected. A check AppTown judges not applicable with a reason is met; errors and checks it could not assess are not. This covers every project, including ones created later, until turned off. Problems are shown in AppTown only."
	}
	bound := "patch and minor dependency updates"
	if categories.Dependencies == "aggressive" {
		bound = "dependency updates, including major versions"
	}
	return "Update level: " + categories.Dependencies + ". " + shown + "It fixes security findings in project dependencies one package at a time and applies " + bound + ". An agent writes any code a change needs; nothing lands unless every check, the tests and the start-up pass. When tests, start-up and scans pass, it lands the change and releases it to the running app on every deployment that follows main and is set to automatic or a window, unless the deployment is protected. It never changes toolchain versions or base images. This covers every project, including ones created later, until a project is turned off. Problems are shown in AppTown only."
}
