package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/flokkit/apptown/internal/fault"
)

// ModelConfig is a model binding's stored configuration. Runtime consumers use
// one decoding shape; capacity policy belongs to Models, provider I/O to inference.
type ModelConfig struct {
	Concurrency int            `json:"concurrency"`
	Model       string         `json:"model"`
	Models      []string       `json:"models"`
	ProviderDef map[string]any `json:"providerDef"`
	BaseURL     string         `json:"baseUrl"`
	API         string         `json:"api"`
	ModelsJSON  string         `json:"modelsJson"`
	APIKeyFrom  []string       `json:"apiKeyFrom"`
	SecretEnv   []string       `json:"secretEnv"`
}

func (b Binding) ModelConfig() (ModelConfig, error) {
	var cfg ModelConfig
	err := json.Unmarshal([]byte(b.ConfigJSON), &cfg)
	return cfg, err
}

// ErrInvalidConfig refuses a binding whose configuration its runtime could
// not read.
var ErrInvalidConfig = fault.New(fault.Invalid, "the binding's configuration is not valid")

// validateConfig refuses, when it is saved, a configuration the runtime
// would refuse when it is used. A model binding the inference gateway cannot
// decode fails every step that asks for a model, and every other binding's
// steps with it (a string "concurrency":"2" was once accepted, then every
// step failed "invalid model binding").
func validateConfig(capability, configJSON string) error {
	if capability == CapLLM {
		cfg, err := strictJSONMap(configJSON)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
		}
		if _, err := parseLLMConfig(cfg); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
		}
		return nil
	}
	if capability != "model" {
		return nil
	}
	var cfg ModelConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		var typed *json.UnmarshalTypeError
		if errors.As(err, &typed) && typed.Field != "" {
			return fmt.Errorf("%w: %q must be %s, not %s", ErrInvalidConfig, typed.Field, typeWords(typed.Type), valueWords(typed.Value))
		}
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if cfg.Concurrency < 0 {
		return fmt.Errorf("%w: \"concurrency\" must be a whole number of steps, 1 or more, or left empty for the default", ErrInvalidConfig)
	}
	return nil
}

func typeWords(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Int, reflect.Int64:
		return "a whole number, such as 2"
	case reflect.String:
		return "text"
	case reflect.Slice:
		return "a list"
	case reflect.Map:
		return "an object"
	}
	return t.String()
}

// valueWords names a JSON value's type as json.UnmarshalTypeError reports it.
func valueWords(v string) string {
	switch v {
	case "string":
		return "text (a quoted value)"
	case "number":
		return "a number"
	case "array":
		return "a list"
	case "object":
		return "an object"
	case "bool":
		return "true or false"
	}
	return v
}
