// Package llmcontract is the unified contract and execution layer for direct structured
// returns: the static Contract is the single source of the structure, and Execute handles
// capability selection, prompt preparation, request retries, Schema/DTO decoding and feedback
// self-healing in one place.
// The protocol is decided before the request goes out; a rejected or violated native request is
// exposed as-is, and silently dropping the schema and resending is forbidden.
package llmcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

// Contract is the static contract of one direct structured return, defined next to each boundary DTO.
type Contract struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Mode is the structured protocol used for this call.
type Mode string

const (
	ModeNativeJSONSchema Mode = "native_json_schema"
	ModePromptContract   Mode = "prompt_contract"
)

// Source is where the capability judgement came from.
type Source string

const (
	SourceConfig  Source = "config"  // Explicitly declared by the user in ModelConfig.json_schema
	SourceAdapter Source = "adapter" // The provider adapter per-model capability table
	SourceUnknown Source = "unknown" // No declaration and unknown capability; conservatively use the prompt contract
)

// Resolution is the protocol choice decided before the request goes out, for caller branching and logs.
type Resolution struct {
	Mode     Mode
	Source   Source
	Strict   bool // Whether strict is carried in native mode
	Provider string
	Model    string
}

// jsonSchemaOverrider is implemented by model wrappers carrying the three-state config override
// (bootstrap.SwappableModel and the wrapper layers that pass it through).
type jsonSchemaOverrider interface {
	JSONSchemaOverride() *bool
}

type modelInfoProvider interface {
	Info() llm.ModelInfo
}

// ModelFacts is the single-instant snapshot one capability resolution needs. Hot-swap wrappers
// implement it so that Resolve reading capability, config override and model identity separately
// cannot pick up state from in between two swaps.
type ModelFacts struct {
	Capabilities       llm.Capabilities
	Info               llm.ModelInfo
	JSONSchemaOverride *bool
}

type modelFactsProvider interface {
	StructuredOutputFacts() ModelFacts
}

// Resolve reads the current model facts on every call (after a hot swap the next call picks up
// the new values): the three-state config wins, then the adapter per-model capability, and
// anything unknown falls back to the prompt contract.
func Resolve(model any) Resolution {
	res := Resolution{Mode: ModePromptContract, Source: SourceUnknown}

	var caps llm.Capabilities
	var info llm.ModelInfo
	var override *bool
	if fp, ok := model.(modelFactsProvider); ok {
		facts := fp.StructuredOutputFacts()
		caps, info, override = facts.Capabilities, facts.Info, facts.JSONSchemaOverride
	} else {
		if cp, ok := model.(llm.CapabilityProvider); ok {
			caps = cp.Capabilities()
		}
		if ip, ok := model.(modelInfoProvider); ok {
			info = ip.Info()
		}
		if o, ok := model.(jsonSchemaOverrider); ok {
			override = o.JSONSchemaOverride()
		}
	}
	res.Provider, res.Model = caps.Provider, caps.Model
	if res.Provider == "" {
		res.Provider = info.Provider
	}
	if res.Model == "" {
		res.Model = info.Name
	}

	if override != nil {
		res.Source = SourceConfig
		if *override {
			res.Mode = ModeNativeJSONSchema
			// When the user declares the endpoint follows the Structured Outputs contract, strict is
			// the default; only when the adapter explicitly says strict is unsupported is the schema
			// sent without it.
			res.Strict = caps.Structured.Strict != llm.SupportNo
		}
		return res
	}

	switch caps.Structured.JSONSchema {
	case llm.SupportYes:
		res.Mode = ModeNativeJSONSchema
		res.Source = SourceAdapter
		res.Strict = caps.Structured.Strict == llm.SupportYes
	case llm.SupportNo:
		res.Source = SourceAdapter
	}
	return res
}

// Plan resolves the protocol and, in native mode, builds the call options; prompt contract mode
// returns nil opts.
func Plan(model any, c Contract) ([]agentcore.CallOption, Resolution) {
	res := Resolve(model)
	if res.Mode != ModeNativeJSONSchema {
		return nil, res
	}
	return []agentcore.CallOption{
		agentcore.WithJSONSchema(c.Name, c.Description, c.Schema, res.Strict),
	}, res
}

// PreparePrompt keeps a single copy of the business-semantic prompt: native mode returns the
// text unchanged, while prompt contract mode generates the format suffix automatically from the
// same Schema. Callers maintain no second template, and a field change cannot fork the prompt
// from response_format.
func PreparePrompt(base string, c Contract, res Resolution) (string, error) {
	if res.Mode != ModePromptContract {
		return base, nil
	}
	schemaJSON, err := json.Marshal(c.Schema)
	if err != nil {
		return "", fmt.Errorf("llmcontract: marshal %s prompt schema: %w", c.Name, err)
	}
	contract := "## Ràng buộc đầu ra\n\n" +
		"Chỉ xuất một đối tượng JSON khớp JSON Schema bên dưới; không xuất giải thích, hàng rào Markdown hay chính các thẻ.\n\n" +
		"<output-json-schema>\n" + string(schemaJSON) + "\n</output-json-schema>"
	if strings.TrimSpace(base) == "" {
		return contract, nil
	}
	return strings.TrimSpace(base) + "\n\n" + contract, nil
}

// Nullable extends a schema's type into a nullable union (["<t>","null"]), used in strict mode
// to express "every field required, optional semantics via null". It returns a copy and never
// mutates the input map.
func Nullable(s map[string]any) map[string]any {
	out := maps.Clone(s)
	if t, ok := out["type"].(string); ok {
		out["type"] = []string{t, "null"}
	}
	switch values := out["enum"].(type) {
	case []string:
		enum := make([]any, 0, len(values)+1)
		for _, value := range values {
			enum = append(enum, value)
		}
		out["enum"] = append(enum, nil)
	case []any:
		enum := slices.Clone(values)
		for _, value := range enum {
			if value == nil {
				return out
			}
		}
		out["enum"] = append(enum, nil)
	}
	return out
}

// ValidateStrictReady recursively checks that a schema meets the structural preconditions of the
// OpenAI strict subset: every object property must be listed in required (optional semantics via
// a null union). litellm performs the same validation at request time and auto-adds
// additionalProperties:false; contract tests assert it up front with this function (RFC §11.1)
// rather than leaving a structural problem to runtime.
func ValidateStrictReady(s map[string]any) error {
	return validateStrictReady(s, "$")
}

func validateStrictReady(s map[string]any, path string) error {
	if typeIncludes(s["type"], "object") {
		props, _ := s["properties"].(map[string]any)
		required, _ := s["required"].([]string)
		for name, sub := range props {
			if !slices.Contains(required, name) {
				return fmt.Errorf("%s.%s chưa được liệt kê trong required (strict yêu cầu mọi thuộc tính đều required)", path, name)
			}
			if subMap, ok := sub.(map[string]any); ok {
				if err := validateStrictReady(subMap, path+"."+name); err != nil {
					return err
				}
			}
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		return validateStrictReady(items, path+"[]")
	}
	return nil
}

func typeIncludes(t any, want string) bool {
	switch v := t.(type) {
	case string:
		return v == want
	case []string:
		return slices.Contains(v, want)
	}
	return false
}

// Fingerprint returns the first 12 hex characters of the sha256 over the schema's canonical JSON,
// for log correlation; encoding/json sorts map keys, so the same contract is naturally stable.
func (c Contract) Fingerprint() string {
	data, err := json.Marshal(c.Schema)
	if err != nil {
		return "unmarshalable"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}
