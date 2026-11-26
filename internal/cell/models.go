package cell

import (
	"fmt"
	"net/url"
)

// The model gateway (E-80). Every agent reaches its model through one
// gateway in the cell; the provider's key stays there. Bootstrap or the API
// writes the provider into Secret ModelSettings; the operator renders the
// gateway's configuration into Secret ModelsSecret, which only the gateway
// mounts, and tells what is set, without the key, in ConfigMap ModelsStatus.
// The API never reads ModelSettings back. Each agent proves itself to the
// gateway with its own key (Secret of its account, key AgentModelKey); the
// gateway holds only its hash.
const (
	ModelSettings = "cell-agent-llm"
	ModelsSecret  = "cell-models"
	ModelsStatus  = "cell-models-status"
	// ModelsStatusKey holds a ModelsView as JSON.
	ModelsStatusKey = "models.json"
	// ModelsVersionKey holds the resourceVersion of ModelSettings the status
	// was made from: a kept key is written only on top of that version.
	ModelsVersionKey = "settingsVersion"
	AgentModelKey    = "modelKey"
	// ModelsURL is where agents reach the gateway; it speaks the Anthropic
	// Messages API and the OpenAI-compatible one.
	ModelsURL = "http://cell-models.cell-system.svc.cluster.local"
	// ModelAlias is the model name agents ask for; the gateway sends the
	// configured model.
	ModelAlias = "default"

	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	// DefaultModel is the Anthropic model without one set (E-33).
	DefaultModel = "claude-opus-5-5"
)

// ModelsView is what the cell tells about its model provider.
type ModelsView struct {
	Configured bool   `json:"configured"`
	Provider   string `json:"provider,omitempty"`
	BaseURL    string `json:"baseUrl,omitempty"`
	Model      string `json:"model,omitempty"`
}

// ModelProvider is the provider in ModelSettings. Settings written before
// the gateway have only apiKey and model: they are Anthropic.
type ModelProvider struct {
	Provider, BaseURL, APIKey, Model string
}

// ReadModelProvider reads Secret ModelSettings' data.
func ReadModelProvider(data map[string][]byte) ModelProvider {
	p := ModelProvider{Provider: string(data["provider"]), BaseURL: string(data["baseUrl"]), APIKey: string(data["apiKey"]), Model: string(data["model"])}
	if p.Provider == "" {
		p.Provider = ProviderAnthropic
	}
	if p.Model == "" && p.Provider == ProviderAnthropic {
		p.Model = DefaultModel
	}
	return p
}

// Check tells what is wrong with the provider, or nil.
func (p ModelProvider) Check() error {
	switch p.Provider {
	case ProviderAnthropic, ProviderOpenAI:
	default:
		return fmt.Errorf("provider: %s or %s", ProviderAnthropic, ProviderOpenAI)
	}
	if p.Model == "" {
		return fmt.Errorf("model: the provider's model name")
	}
	if p.BaseURL != "" {
		u, err := url.Parse(p.BaseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("baseUrl: an http(s) address such as https://models.example.com/v1")
		}
	}
	return nil
}

// View is the provider without its key.
func (p ModelProvider) View() ModelsView {
	return ModelsView{Configured: p.APIKey != "" || p.Provider == ProviderOpenAI && p.BaseURL != "", Provider: p.Provider, BaseURL: p.BaseURL, Model: p.Model}
}
