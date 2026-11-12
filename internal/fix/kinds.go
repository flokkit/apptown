package fix

import "slices"

// Action and Classifier name reusable execution mechanisms, independently of
// the kind that selects them.
type Action string
type Classifier string

const (
	ScriptAction     Action     = "script"
	UpdateClassifier Classifier = "dependency-update"
)

type DeclarationField struct {
	Name     string
	Required bool
}

type Kind struct {
	Action            Action
	Classifier        Classifier
	DeclarationFields []DeclarationField
}

// kinds is the registration point for mechanical fixes. Consumers use the
// selected mechanisms and fields rather than recognizing individual kind names.
var kinds = map[string]Kind{
	"update": {Action: ScriptAction, Classifier: UpdateClassifier, DeclarationFields: []DeclarationField{{"ecosystem", true}, {"run", true}, {"runFor", false}, {"list", false}, {"files", false}}},
}

func Lookup(kind string) (Kind, bool) {
	k, ok := kinds[kind]
	return k, ok
}

func SupportsTargets(d Declaration) bool {
	k, ok := Lookup(d.Kind)
	return ok && k.Classifier == UpdateClassifier && d.Ecosystem != ""
}

// Names returns the registered kinds in stable order for schema consumers.
func Names() []string {
	names := make([]string, 0, len(kinds))
	for name := range kinds {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
