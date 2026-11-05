package duoworkflow

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
)

// buildersCapture is testdata/gitlab_duo_agent_builders.json: NeoPi's OWN
// workflow builders over chat bodies parsed by openai-chat-server parseRequest
// (capture_neopi_gitlab_duo_agent_builders.ts).
type buildersCapture struct {
	Scenarios []struct {
		Name   string `json:"name"`
		Body   string `json:"body"`
		Starts []struct {
			Variant  string `json:"variant"`
			Start    string `json:"start"`
			Approval string `json:"approval"`
		} `json:"starts"`
		NoTools string `json:"noTools"`
	} `json:"scenarios"`
	WebSocket []struct {
		BaseURL string `json:"baseUrl"`
		Options struct {
			ProjectID               string `json:"projectId"`
			NamespaceID             string `json:"namespaceId"`
			RootNamespaceID         string `json:"rootNamespaceId"`
			SelectedModelIdentifier string `json:"selectedModelIdentifier"`
			WorkflowDefinition      string `json:"workflowDefinition"`
			ServiceEndpoint         bool   `json:"serviceEndpoint"`
		} `json:"options"`
		URL string `json:"url"`
	} `json:"webSocket"`
	WebSocketHeaders []struct {
		Options struct {
			Token           string            `json:"token"`
			BaseURL         string            `json:"baseUrl"`
			ProjectID       string            `json:"projectId"`
			NamespaceID     string            `json:"namespaceId"`
			RootNamespaceID string            `json:"rootNamespaceId"`
			ExtraHeaders    map[string]string `json:"extraHeaders"`
		} `json:"options"`
		Headers map[string]string `json:"headers"`
	} `json:"webSocketHeaders"`
	CreateBodies []struct {
		NamespaceID string `json:"namespaceId"`
		Options     struct {
			ProjectID string `json:"projectId"`
			Goal      string `json:"goal"`
		} `json:"options"`
		Body string `json:"body"`
	} `json:"createBodies"`
	DirectAccessBodies []struct {
		RootNamespaceID string `json:"rootNamespaceId"`
		ProjectID       string `json:"projectId"`
		Body            string `json:"body"`
	} `json:"directAccessBodies"`
	SettingsBody string `json:"settingsBody"`
	StopBody     string `json:"stopBody"`
	ToolResults  []struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
		} `json:"content"`
		IsError bool   `json:"isError"`
		Answer  string `json:"answer"`
	} `json:"toolResults"`
}

func loadBuildersCapture(t *testing.T) buildersCapture {
	t.Helper()
	data, err := os.ReadFile("testdata/gitlab_duo_agent_builders.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture buildersCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

// The WebSocket start request NeoPi sends for each conversation: the goal
// transcript, the sanitized system prompt in the inline flow, the MCP tools
// and the metadata, byte for byte, with and without tools, and its approval.
func TestStartRequestMatchesNeoPi(t *testing.T) {
	variants := map[string]struct {
		pinned   string
		metadata StartMetadata
	}{
		"full": {"claude_opus_4_8", StartMetadata{ProjectID: "4242", NamespaceID: "gid://gitlab/Group/70", RootNamespaceID: "70"}},
		"bare": {"", StartMetadata{}},
	}
	for _, scenario := range loadBuildersCapture(t).Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			parsed, err := cursorwire.ParseOpenAIChat([]byte(scenario.Body), func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			for _, start := range scenario.Starts {
				variant := variants[start.Variant]
				request, err := BuildStartRequest("wf-123", parsed.ModelID, &parsed.Context, parsed.Context.Tools, variant.pinned, variant.metadata)
				if err != nil {
					t.Fatal(err)
				}
				if got := string(request.JSON(false)); got != start.Start {
					t.Fatalf("%s start request:\n got %s\nwant %s", start.Variant, got, start.Start)
				}
				if got := string(request.JSON(true)); got != start.Approval {
					t.Fatalf("%s approval request:\n got %s\nwant %s", start.Variant, got, start.Approval)
				}
			}
			request, err := BuildStartRequest("wf-123", parsed.ModelID, &parsed.Context, nil, "", StartMetadata{})
			if err != nil {
				t.Fatal(err)
			}
			if got := string(request.JSON(false)); got != scenario.NoTools {
				t.Fatalf("start request without tools:\n got %s\nwant %s", got, scenario.NoTools)
			}
		})
	}
}

// The socket address, its headers, the REST bodies and a tool result's
// answer, as NeoPi builds them.
func TestWorkflowWireMatchesNeoPi(t *testing.T) {
	capture := loadBuildersCapture(t)
	for _, entry := range capture.WebSocket {
		options := SocketOptions(entry.Options)
		if got := WebSocketURL(entry.BaseURL, options); got != entry.URL {
			t.Fatalf("WebSocketURL(%q) = %s, NeoPi %s", entry.BaseURL, got, entry.URL)
		}
	}
	for _, entry := range capture.WebSocketHeaders {
		o := entry.Options
		got := WebSocketHeaders(o.Token, o.BaseURL, SocketOptions{ProjectID: o.ProjectID, NamespaceID: o.NamespaceID, RootNamespaceID: o.RootNamespaceID}, o.ExtraHeaders)
		if !reflect.DeepEqual(got, entry.Headers) {
			t.Fatalf("WebSocketHeaders = %v, NeoPi %v", got, entry.Headers)
		}
	}
	for _, entry := range capture.CreateBodies {
		if got := string(CreateBody("", entry.NamespaceID, entry.Options.ProjectID, entry.Options.Goal)); got != entry.Body {
			t.Fatalf("CreateBody = %s, NeoPi %s", got, entry.Body)
		}
	}
	for _, entry := range capture.DirectAccessBodies {
		if got := string(DirectAccessBody("", entry.RootNamespaceID, entry.ProjectID)); got != entry.Body {
			t.Fatalf("DirectAccessBody = %s, NeoPi %s", got, entry.Body)
		}
	}
	if SettingsBody != capture.SettingsBody || StopBody != capture.StopBody {
		t.Fatalf("settings %s stop %s, NeoPi %s %s", SettingsBody, StopBody, capture.SettingsBody, capture.StopBody)
	}
	for _, entry := range capture.ToolResults {
		message := cursorwire.Message{Role: "toolResult", IsError: entry.IsError}
		for _, item := range entry.Content {
			message.Parts = append(message.Parts, cursorwire.Part{Image: item.Type == "image", Text: item.Text, MimeType: item.MimeType})
		}
		if got := string(ToolResultAnswer(message)); got != entry.Answer {
			t.Fatalf("ToolResultAnswer = %s, NeoPi %s", got, entry.Answer)
		}
	}
}
