package thinking

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CanonicalCodexWireEffort maps a logical Codex reasoning preset to the value
// used by the official Codex upstream request. The mapping is model-aware:
// only known, non-user-defined models that advertise Ultra use max on the
// wire. Non-Ultra values and custom-model values are preserved verbatim.
func CanonicalCodexWireEffort(effort string, modelInfo *registry.ModelInfo) string {
	if !strings.EqualFold(strings.TrimSpace(effort), string(LevelUltra)) {
		return effort
	}
	if IsUserDefinedModel(modelInfo) || modelInfo.Thinking == nil || !HasLevel(modelInfo.Thinking.Levels, string(LevelUltra)) {
		return effort
	}
	return string(LevelMax)
}

// CanonicalCodexWireEffortForModel resolves Codex model capability before
// applying CanonicalCodexWireEffort. Unknown and user-defined models remain
// passthrough so private upstream semantics are not redefined by this patch.
func CanonicalCodexWireEffortForModel(effort, model string) string {
	baseModel := ParseSuffix(model).ModelName
	return CanonicalCodexWireEffort(effort, registry.LookupModelInfo(baseModel, "codex"))
}

// NormalizeCodexReasoningEffortForWire validates and canonicalizes the final
// reasoning.effort after payload rules have run. It intentionally applies only
// to known, non-user-defined Codex models so custom upstreams can continue to
// accept provider-specific values such as a literal ultra.
func NormalizeCodexReasoningEffortForWire(body []byte, model string) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, nil
	}

	effortResult := gjson.GetBytes(body, "reasoning.effort")
	if !effortResult.Exists() || effortResult.Type != gjson.String {
		return body, nil
	}
	requestedEffort := strings.ToLower(strings.TrimSpace(effortResult.String()))
	if requestedEffort != string(LevelUltra) {
		return body, nil
	}

	baseModel := ParseSuffix(model).ModelName
	modelInfo := registry.LookupModelInfo(baseModel, "codex")
	if IsUserDefinedModel(modelInfo) {
		return body, nil
	}
	if modelInfo.Thinking == nil || !HasLevel(modelInfo.Thinking.Levels, string(LevelUltra)) {
		message := fmt.Sprintf("level %q not supported", requestedEffort)
		if modelInfo.Thinking != nil && len(modelInfo.Thinking.Levels) > 0 {
			message = fmt.Sprintf("level %q not supported, valid levels: %s", requestedEffort, strings.Join(modelInfo.Thinking.Levels, ", "))
		}
		return body, NewThinkingErrorWithModel(ErrLevelNotSupported, message, modelInfo.ID)
	}

	result, err := sjson.SetBytes(body, "reasoning.effort", CanonicalCodexWireEffort(effortResult.String(), modelInfo))
	if err != nil {
		return body, err
	}
	return result, nil
}
