// Package openai is the hosted OpenAI Chat Completions llm.Provider.
//
// It is pure Go — available in every build. The OpenAI wire format,
// the json_schema structured-output mechanism, and the hollow-200
// retry all live in the shared openaicompat.Client; this package is
// just the constructor that addresses api.openai.com with a Bearer
// key. Azure OpenAI and user-registered custom OpenAI-compatible
// endpoints reuse the same core through their own constructors.
package openai

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zzet/gortex/internal/llm"
	"github.com/zzet/gortex/internal/llm/provider/openaicompat"
)

// New constructs the OpenAI provider. The API key is read from the env
// var named by cfg.APIKeyEnv (default OPENAI_API_KEY); an unset key is
// a hard error.
func New(cfg llm.RemoteConfig) (llm.Provider, error) {
	keyEnv := strings.TrimSpace(cfg.APIKeyEnv)
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	key := strings.TrimSpace(os.Getenv(keyEnv))
	if key == "" {
		return nil, fmt.Errorf("openai: API key env %q is not set", keyEnv)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("openai: llm.openai.model is empty")
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	return &openaicompat.Client{
		ProviderID:      "openai",
		Tag:             "openai",
		Model:           cfg.Model,
		URL:             base + "/v1/chat/completions",
		Headers:         map[string]string{"authorization": "Bearer " + key},
		HTTPClient:      &http.Client{Timeout: 120 * time.Second},
		SchemaMode:      openaicompat.SchemaJSONSchema,
		ReasoningEffort: strings.TrimSpace(cfg.Effort),
	}, nil
}
