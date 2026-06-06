package openaicompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/llm"
)

// newTestClient wires a Client at a test server with the given mode.
func newTestClient(url string, mode SchemaMode) *Client {
	return &Client{
		ProviderID: "custom",
		Tag:        "custom:test",
		Model:      "m",
		URL:        url + "/v1/chat/completions",
		Headers:    map[string]string{"authorization": "Bearer k"},
		HTTPClient: http.DefaultClient,
		SchemaMode: mode,
	}
}

func TestComplete_JSONObjectModeExtractsFromChattyReply(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		// A chatty reply that wraps the JSON in prose + a markdown fence
		// — the json_object / prompt-rider path must still recover it.
		content := "Sure!\n```json\n{\"terms\":[\"jwt\"]}\n```"
		reply, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
		_, _ = w.Write(reply)
	}))
	defer srv.Close()

	p := newTestClient(srv.URL, SchemaJSONObject)
	resp, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "Query: auth"}},
		Shape:    llm.ShapeExpandTerms,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"terms":["jwt"]}` {
		t.Errorf("text=%q want the extracted JSON object", resp.Text)
	}
	rf, ok := gotBody["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_object" {
		t.Errorf("json_object mode must send response_format type=json_object, got %v", gotBody["response_format"])
	}
	// The schema rider must ride on the prompt, not response_format.
	msgs := gotBody["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)["content"].(string)
	if !strings.Contains(last, "JSON Schema") {
		t.Errorf("expected a schema rider appended to the last message, got %q", last)
	}
}

func TestComplete_PromptOnlyModeSendsNoResponseFormat(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"order\":[\"a\"]}"}}]}`)
	}))
	defer srv.Close()

	p := newTestClient(srv.URL, SchemaPromptOnly)
	resp, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "rank these"}},
		Shape:    llm.ShapeRerankOrder,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"order":["a"]}` {
		t.Errorf("text=%q", resp.Text)
	}
	if _, ok := gotBody["response_format"]; ok {
		t.Error("prompt-only mode must not send response_format")
	}
}

func TestComplete_HonoursCustomTokenFieldTemperatureEffortExtraBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "secret" {
			t.Errorf("api-key header=%q", r.Header.Get("api-key"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	temp := 0.2
	p := &Client{
		ProviderID:      "custom",
		Tag:             "custom:test",
		Model:           "m",
		URL:             srv.URL + "/v1/chat/completions",
		Headers:         map[string]string{"api-key": "secret"},
		HTTPClient:      http.DefaultClient,
		SchemaMode:      SchemaPromptOnly,
		MaxTokensField:  "max_tokens",
		Temperature:     &temp,
		ReasoningEffort: "high",
		ExtraBody:       map[string]any{"provider": map[string]any{"order": []any{"groq"}}},
	}
	if _, err := p.Complete(context.Background(), llm.CompletionRequest{
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxTokens: 128,
	}); err != nil {
		t.Fatal(err)
	}
	if gotBody["max_tokens"] != float64(128) {
		t.Errorf("expected legacy max_tokens=128, got %v (max_completion_tokens=%v)", gotBody["max_tokens"], gotBody["max_completion_tokens"])
	}
	if gotBody["temperature"] != 0.2 {
		t.Errorf("temperature=%v want 0.2", gotBody["temperature"])
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort=%v want high", gotBody["reasoning_effort"])
	}
	if _, ok := gotBody["provider"]; !ok {
		t.Error("ExtraBody fields must be merged into the request body")
	}
}

func TestName_FallsBackToTag(t *testing.T) {
	c := &Client{Tag: "custom:foo"}
	if c.Name() != "custom:foo" {
		t.Errorf("Name()=%q want the Tag fallback", c.Name())
	}
	c.ProviderID = "custom"
	if c.Name() != "custom" {
		t.Errorf("Name()=%q want ProviderID", c.Name())
	}
}

func TestWithSchemaRider_AppendsTrailingUserTurnAfterAssistant(t *testing.T) {
	in := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleAssistant, Content: "prior"},
	}
	out := withSchemaRider(in, llm.ShapeExpandTerms, nil)
	if len(out) != 3 {
		t.Fatalf("expected a trailing user turn appended, got %d messages", len(out))
	}
	if out[2].Role != llm.RoleUser || !strings.Contains(out[2].Content, "JSON Schema") {
		t.Errorf("trailing turn = %+v", out[2])
	}
	// The original slice must be untouched.
	if len(in) != 2 {
		t.Error("withSchemaRider mutated its input")
	}
}
