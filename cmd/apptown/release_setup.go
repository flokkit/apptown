package main

import "fmt"

type releaseSetupTarget struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	DisplayLabel     string `json:"displayLabel"`
	SettingsRevision int64  `json:"settingsRevision"`
}

type releaseSetupPreference struct {
	DeploymentID     string `json:"deploymentId"`
	Revision         int64  `json:"revision"`
	EligibilityIssue string `json:"eligibilityIssue"`
	LegacySetup      *struct {
		TargetID       string `json:"targetId"`
		DeploymentName string `json:"deploymentName"`
	} `json:"legacySetup"`
}

// Setup has its own explicit save acknowledgement. Reads and releases never
// infer the first target or silently create a deployment/default preference.
func cmdReleaseSetup(args []string) error {
	fs := newFlags("release-setup")
	targetID := fs.String("target", "", "offered environment UUID (defaults only to an imported saved choice)")
	name := fs.String("name", "", "deployment name (defaults only to an imported saved choice)")
	promotion := fs.Bool("require-promotion", false, "require promotion permission in addition to deployment permission")
	save := fs.Bool("save", false, "save this release setup; does not start a release")
	asJSON := fs.Bool("json", false, "print the observed setup choices or saved preference as JSON")
	args = parseArgs(fs, args)
	if len(args) != 1 {
		return usagef("usage: apptown release-setup <project> [--target UUID --name name] [--require-promotion] [--save] [--json]")
	}
	slug := args[0]
	path := "/projects/" + slug + "/release/default"
	var settings releaseSetupPreference
	if err := api.get(path, &settings); err != nil {
		return err
	}
	var offered struct {
		Targets []releaseSetupTarget `json:"targets"`
	}
	if err := api.get("/projects/"+slug+"/targets", &offered); err != nil {
		return err
	}
	if settings.LegacySetup != nil {
		if *targetID == "" {
			*targetID = settings.LegacySetup.TargetID
		}
		if *name == "" {
			*name = settings.LegacySetup.DeploymentName
		}
		*promotion = true
	}
	if !*save {
		if *asJSON {
			return printJSON(struct {
				Preference releaseSetupPreference `json:"preference"`
				Targets    []releaseSetupTarget   `json:"targets"`
			}{settings, offered.Targets})
		}
		switch {
		case settings.DeploymentID != "":
			fmt.Printf("Release preference is configured as %s.\n", settings.DeploymentID)
		case settings.LegacySetup != nil:
			fmt.Printf("Imported choice: environment %s, deployment %s; it still needs explicit setup.\n", *targetID, *name)
		case settings.Revision > 0:
			fmt.Println("The release preference was explicitly cleared.")
		default:
			fmt.Println("No release preference has been configured.")
		}
		if settings.EligibilityIssue != "" {
			fmt.Println(settings.EligibilityIssue)
		}
		for _, target := range offered.Targets {
			label := target.DisplayLabel
			if label == "" {
				label = target.Name
			}
			fmt.Printf("  %s  %s (settings revision %d)\n", target.ID, label, target.SettingsRevision)
		}
		fmt.Println("Use --target UUID --name deployment --save to save a choice. No release has started.")
		return nil
	}
	if *targetID == "" || *name == "" {
		return usagef("setup not saved: explicitly choose --target UUID and --name; no release has started")
	}
	var target *releaseSetupTarget
	for i := range offered.Targets {
		if offered.Targets[i].ID == *targetID {
			target = &offered.Targets[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("setup not saved: selected environment is not currently offered; choose one explicitly; no release has started")
	}
	var saved releaseSetupPreference
	if err := api.post(path+"/setup", map[string]any{"targetId": target.ID, "targetRevision": target.SettingsRevision, "deploymentName": *name, "expectedRevision": settings.Revision, "requirePromotion": *promotion}, &saved); err != nil {
		return fmt.Errorf("setup was not confirmed; setup does not start a release: %w", err)
	}
	if *asJSON {
		return printJSON(saved)
	}
	fmt.Println("Release preference saved. No release has started; review the release separately.")
	return nil
}
