package check

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Applicability. A check declares what it applies to (Declaration.AppliesTo,
// Scanner.AppliesTo) in words about the app's declared parts, and the judge
// decides before anything runs whether it applies: never the check's own
// script searching the tree. A check that applies to nothing is
// NotApplicable with the reason; one that applies runs once per folder of
// the parts it matches, and finding nothing to judge there is the check's
// failure, since the declaration and the code disagree.
//
// The words:
//
//	repository  once, at the repository root, when the tree holds any file
//	part        any part
//	build       a part built from a folder's Dockerfile
//	static      a part served as files
//	container   a part run as a container
//	tool:<name> a part built with that tool
//
// A part matches when it matches any of the words; repository stands alone.
const (
	AppliesRepository = "repository"
	AppliesPart       = "part"
	AppliesBuild      = "build"
	AppliesStatic     = "static"
	AppliesContainer  = "container"
	appliesTool       = "tool:"
)

// Part kinds, as Part.Kinds lists them.
const (
	KindBuild     = "build"
	KindStatic    = "static"
	KindContainer = "container"
)

// Part is one part of the app as its contract declares it, in the terms
// applicability reads. The caller derives it from its own contract.
type Part struct {
	Name string
	// Folder is the part's folder relative to the repository root ("." for
	// the root); "" for a part with none (a pulled image), which is judged
	// at the root.
	Folder string
	Kinds  []string // KindBuild, KindStatic, KindContainer
	Tools  []string // what the part is built with
	Public bool
}

// Place is one folder a check runs in and the parts judged there.
type Place struct {
	Folder string   // relative to the repository root; "." is the root
	Parts  []string // sorted; nil for a repository run
}

// ValidateAppliesTo checks a declared list of words. Tool names are only
// checked for their spelling here: which tools exist is the caller's.
func ValidateAppliesTo(words []string) error {
	if len(words) == 0 {
		return fmt.Errorf("appliesTo is required: repository, part, build, static, container or tool:<name>")
	}
	seen := map[string]bool{}
	for _, w := range words {
		if seen[w] {
			return fmt.Errorf("appliesTo names %q twice", w)
		}
		seen[w] = true
		switch {
		case w == AppliesRepository:
			if len(words) > 1 {
				return fmt.Errorf("appliesTo: repository stands alone")
			}
		case w == AppliesPart, w == AppliesBuild, w == AppliesStatic, w == AppliesContainer:
		case strings.HasPrefix(w, appliesTool):
			if !ValidToolName(strings.TrimPrefix(w, appliesTool)) {
				return fmt.Errorf("appliesTo: %q is not a tool name (lower case letters, digits and dashes)", w)
			}
		default:
			return fmt.Errorf("appliesTo: %q is not one of repository, part, build, static, container, tool:<name>", w)
		}
	}
	return nil
}

// ValidToolName is the spelling a tool name has, in appliesTo and in a
// contract's part tools.
func ValidToolName(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// AppliesTools names the tools a list of words names.
func AppliesTools(words []string) []string {
	var out []string
	for _, w := range words {
		if t, ok := strings.CutPrefix(w, appliesTool); ok {
			out = append(out, t)
		}
	}
	return out
}

// matches says whether a part matches any of the words.
func matches(p Part, words []string) bool {
	for _, w := range words {
		switch {
		case w == AppliesPart:
			return true
		case w == AppliesBuild || w == AppliesStatic || w == AppliesContainer:
			if slices.Contains(p.Kinds, w) {
				return true
			}
		case strings.HasPrefix(w, appliesTool):
			if slices.Contains(p.Tools, strings.TrimPrefix(w, appliesTool)) {
				return true
			}
		}
	}
	return false
}

// Applies is the one applicability decision: the folders a check with
// these words runs in, or the reason it is not applicable (no places), or
// the error that makes it unjudgeable (a contract that could not be read).
func Applies(words []string, src Source) ([]Place, string, error) {
	if len(words) == 0 {
		return nil, "", fmt.Errorf("the check declares no appliesTo")
	}
	if len(words) == 1 && words[0] == AppliesRepository {
		if src.Empty {
			return nil, "the repository holds no files", nil
		}
		return []Place{{Folder: "."}}, "", nil
	}
	if src.PartsErr != nil {
		return nil, "", fmt.Errorf("the app contract could not be read: %w", src.PartsErr)
	}
	if len(src.Parts) == 0 {
		if src.NoContract {
			return nil, "the app declares no parts: there is no .apptown/app.yaml", nil
		}
		return nil, "the app contract declares no parts", nil
	}
	byFolder := map[string][]string{}
	for _, p := range src.Parts {
		if !matches(p, words) {
			continue
		}
		f := p.Folder
		if f == "" {
			f = "."
		}
		byFolder[f] = append(byFolder[f], p.Name)
	}
	if len(byFolder) == 0 {
		return nil, notMatched(words, src.Parts), nil
	}
	var out []Place
	for f, names := range byFolder {
		sort.Strings(names)
		out = append(out, Place{Folder: f, Parts: names})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Folder < out[j].Folder })
	return out, "", nil
}

// AppliesToPublic is the decision for a check that looks at the public
// part's image (an image or exec-image scanner): the public part must run
// as a container, and match the words.
func AppliesToPublic(words []string, src Source) (string, error) {
	if len(words) == 0 {
		return "", fmt.Errorf("the check declares no appliesTo")
	}
	if src.PartsErr != nil {
		return "", fmt.Errorf("the app contract could not be read: %w", src.PartsErr)
	}
	var pub *Part
	for i := range src.Parts {
		if src.Parts[i].Public {
			pub = &src.Parts[i]
		}
	}
	switch {
	case pub == nil && src.NoContract:
		return "the app declares no parts: there is no .apptown/app.yaml, so there is no image", nil
	case pub == nil:
		return "the app contract declares no public part, so there is no image", nil
	case !slices.Contains(pub.Kinds, KindContainer):
		return "the public part is a static site: there is no image", nil
	}
	if len(words) == 1 && words[0] == AppliesRepository {
		return "", nil
	}
	if !matches(*pub, words) {
		return "the public part " + pub.Name + " is not " + describeWords(words) + " (" + partList([]Part{*pub}) + ")", nil
	}
	return "", nil
}

func notMatched(words []string, parts []Part) string {
	return "no part is " + describeWords(words) + " (the contract's parts: " + partList(parts) + ")"
}

func describeWords(words []string) string {
	var tools, kinds []string
	for _, w := range words {
		switch {
		case strings.HasPrefix(w, appliesTool):
			tools = append(tools, strings.TrimPrefix(w, appliesTool))
		case w == AppliesBuild:
			kinds = append(kinds, "built from a Dockerfile")
		case w == AppliesStatic:
			kinds = append(kinds, "a static site")
		case w == AppliesContainer:
			kinds = append(kinds, "run as a container")
		case w == AppliesPart:
			kinds = append(kinds, "declared")
		}
	}
	if len(tools) > 0 {
		kinds = append(kinds, "built with "+orList(tools))
	}
	return orList(kinds)
}

func orList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " or " + xs[len(xs)-1]
}

func partList(parts []Part) string {
	var out []string
	for _, p := range parts {
		var what []string
		if slices.Contains(p.Kinds, KindStatic) {
			what = append(what, "a static site")
		} else if slices.Contains(p.Kinds, KindContainer) {
			what = append(what, "a container")
		}
		if len(p.Tools) > 0 {
			what = append(what, "built with "+strings.Join(p.Tools, " and "))
		} else {
			what = append(what, "naming no tools")
		}
		out = append(out, p.Name+", "+strings.Join(what, ", "))
	}
	return strings.Join(out, "; ")
}

// placeEnv is the environment a check run in a place gets: the folder, the
// parts judged there, and the contract's parts for checks that judge the
// contract itself.
func placeEnv(pl Place, src Source) []string {
	var table []string
	for _, p := range src.Parts {
		f := p.Folder
		if f == "" {
			f = "."
		}
		table = append(table, p.Name+"\t"+f+"\t"+strings.Join(p.Tools, ","))
	}
	return []string{
		"APPTOWN_FOLDER=" + pl.Folder,
		"APPTOWN_PARTS_HERE=" + strings.Join(pl.Parts, " "),
		"APPTOWN_PARTS=" + strings.Join(table, "\n"),
		"APPTOWN_NOT_APP_CODE=" + strings.Join(src.NotAppCode, "\n"),
	}
}

// placeLabel prefixes what a per-part run reports: the parts it judged.
func placeLabel(pl Place) string {
	if pl.Parts == nil {
		return ""
	}
	return strings.Join(pl.Parts, ", ") + ": "
}

// notApplicable is the evaluation of a check that applies to nothing here.
func notApplicable(d Declared, mode Mode, why string) Evaluation {
	return Evaluation{Declared: d, Mode: mode, Outcome: NotApplicable, Detail: why}
}

// worse orders outcomes for one check judged in several folders: an error
// over a failure over a pass.
func worse(a, b Outcome) Outcome {
	rank := func(o Outcome) int {
		switch o {
		case Error:
			return 3
		case CannotAssess:
			return 2
		case DoesNotConform:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// combine folds one check's evaluations in several places into its one
// evaluation: the worst outcome, each place's words prefixed with its parts.
// A guidance finding is one per check (its fingerprint is the check's), so
// several places' findings become one carrying every place's words and a
// recorded exception keeps matching; a tool's own findings keep theirs.
func combine(d Declared, mode Mode, places []Place, evs []Evaluation) Evaluation {
	if len(evs) == 1 && places[0].Parts == nil {
		return evs[0]
	}
	out := Evaluation{Declared: d, Mode: mode, Outcome: Conforms}
	var details, outputs, confDetails, confItems []string
	var conf *Finding
	worst := -1
	for i, ev := range evs {
		label := placeLabel(places[i])
		if worst < 0 || worse(evs[worst].Outcome, ev.Outcome) != evs[worst].Outcome {
			worst = i
		}
		out.Outcome = worse(out.Outcome, ev.Outcome)
		if ev.Detail != "" {
			details = append(details, label+ev.Detail)
		}
		if ev.Evidence.Output != "" {
			outputs = append(outputs, label+ev.Evidence.Output)
		}
		for _, o := range ev.Occurrences {
			out.Occurrences = append(out.Occurrences, label+o)
		}
		out.Evidence.Duration += ev.Evidence.Duration
		if ev.Evidence.Report != "" {
			out.Evidence.Report = ev.Evidence.Report
		}
		out.Fault = out.Fault || ev.Fault
		for _, f := range ev.Findings {
			if f.Kind != "conformance" {
				out.Findings = append(out.Findings, f)
				continue
			}
			if conf == nil {
				c := f
				conf = &c
			}
			if f.Detail != "" {
				confDetails = append(confDetails, label+f.Detail)
			}
			for _, it := range f.Items {
				confItems = append(confItems, label+it)
			}
		}
	}
	out.Evidence.ExitCode = evs[worst].Evidence.ExitCode
	out.Evidence.Image = evs[worst].Evidence.Image
	out.Detail = strings.Join(details, "; ")
	out.Evidence.Output = strings.Join(outputs, "\n")
	if conf != nil {
		conf.Detail = strings.Join(confDetails, "\n")
		conf.Items = confItems
		out.Findings = append([]Finding{*conf}, out.Findings...)
	}
	return out
}

// AppliesHere is the decision for a check judged once for the whole app (a
// rubric, a runtime probe): "" when it applies to some declared part (or
// the repository), else the reason it is not applicable.
func AppliesHere(words []string, src Source) (string, error) {
	places, why, err := Applies(words, src)
	if err != nil || len(places) > 0 {
		return "", err
	}
	return why, nil
}

// NotApplicableHere is the evaluation of a check judged once for the app
// when it does not apply, or nil when it does (an error is the check's
// error, never a pass).
func NotApplicableHere(d Declared, src Source, mode Mode) *Evaluation {
	why, err := AppliesHere(d.AppliesTo, src)
	switch {
	case err != nil:
		return &Evaluation{Declared: d, Mode: mode, Outcome: Error, Detail: err.Error()}
	case why != "":
		ev := notApplicable(d, mode, why)
		return &ev
	}
	return nil
}

// ToolPlaces intersects a check's applicability with its scanner's limit.
// A repository declaration imposes no part limit; either declaration may
// narrow it to parts. Two part declarations must match the same part.
func ToolPlaces(words, limit []string, src Source) ([]Place, string, error) {
	places, why, err := Applies(words, src)
	if err != nil || len(places) == 0 || len(limit) == 0 {
		return places, why, err
	}
	limited, why, err := Applies(limit, src)
	if err != nil || len(limited) == 0 {
		return nil, why, err
	}
	if len(words) == 1 && words[0] == AppliesRepository {
		return limited, "", nil
	}
	if len(limit) == 1 && limit[0] == AppliesRepository {
		return places, "", nil
	}
	both := src
	both.Parts = nil
	for _, p := range src.Parts {
		if matches(p, words) && matches(p, limit) {
			both.Parts = append(both.Parts, p)
		}
	}
	return Applies(words, both)
}

// ToolPublic limits an image check to a public part matching both declarations.
func ToolPublic(words, limit []string, src Source) (string, error) {
	why, err := AppliesToPublic(words, src)
	if why != "" || err != nil || len(limit) == 0 {
		return why, err
	}
	return AppliesToPublic(limit, src)
}
