package duoworkflow

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiversion"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// availableModelsQuery is GITLAB_DUO_WORKFLOW_AVAILABLE_MODELS_QUERY.
const availableModelsQuery = `query omp_gitlabDuoWorkflowAvailableModels($rootNamespaceId: GroupID!) {
  aiChatAvailableModels(rootNamespaceId: $rootNamespaceId) {
    defaultModel { name ref }
    selectableModels { name ref }
    pinnedModel { name ref }
  }
}`

// projectQuery is discoverGitLabDuoWorkflowProject's listing query.
const projectQuery = "per_page=1&min_access_level=30&order_by=last_activity_at&sort=desc"

// userAgent is the transport fetch's User-Agent NeoPi's REST calls carry.
func userAgent() string { return "npi/" + neopiversion.Version }

// rest issues the workflow's GitLab REST calls as NeoPi's fetch puts them on
// the wire. NeoPi bounds each with a 30 s deadline; this proxy sets none
// after a connection is made, so a call ends with the request's context.
type rest struct {
	client  *http.Client
	baseURL string
	apiKey  string
}

type restAnswer struct {
	status int
	body   []byte
}

func (a restAnswer) ok() bool { return a.status >= 200 && a.status < 300 }

// json is response.json(): ok is false for a body that is no JSON.
func (a restAnswer) json() (any, bool) {
	return cursorwire.ParseJSON(string(a.body))
}

func (r *rest) do(ctx context.Context, method, path string, body []byte) (restAnswer, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, reader)
	if err != nil {
		return restAnswer{}, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent())
	client := r.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return restAnswer{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	answer := restAnswer{status: resp.StatusCode}
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return answer, err
	}
	defer func() { _ = decoded.Close() }()
	answer.body, err = io.ReadAll(decoded)
	return answer, err
}

// ensureSettings is ensureGitLabDuoWorkflowSettings: the best-effort PUT
// turning on the namespace flags the inline flow needs. It reports whether
// the attempt was definitive: any answer below 500.
func (r *rest) ensureSettings(ctx context.Context, restNamespaceID string) bool {
	answer, err := r.do(ctx, http.MethodPut, groupsPath+"/"+encodeURIComponent(restNamespaceID), []byte(SettingsBody))
	if err != nil && answer.status == 0 {
		return false
	}
	return answer.status < 500
}

type project struct {
	id, path string
}

// discoverProject is discoverGitLabDuoWorkflowProject: the namespace's most
// recently active project the token can develop in, else any of its
// membership projects.
func (r *rest) discoverProject(ctx context.Context, restNamespaceID string) *project {
	for _, endpoint := range []string{
		groupsPath + "/" + encodeURIComponent(restNamespaceID) + "/projects?include_subgroups=true&" + projectQuery,
		projectsPath + "?membership=true&" + projectQuery,
	} {
		answer, err := r.do(ctx, http.MethodGet, endpoint, nil)
		if err != nil || !answer.ok() {
			continue
		}
		payload, ok := answer.json()
		if !ok {
			continue
		}
		items, _ := payload.([]any)
		if len(items) == 0 {
			continue
		}
		first := asRecord(items[0])
		id, _ := getRecordString(first, "id")
		path, _ := getRecordString(first, "path_with_namespace")
		if id != "" && path != "" {
			return &project{id: id, path: path}
		}
	}
	return nil
}

// numericProjectID is resolveGitLabDuoWorkflowNumericProjectId.
func (r *rest) numericProjectID(ctx context.Context, projectPath string) string {
	answer, err := r.do(ctx, http.MethodGet, projectsPath+"/"+encodeURIComponent(projectPath), nil)
	if err != nil || !answer.ok() {
		return ""
	}
	payload, ok := answer.json()
	if !ok {
		return ""
	}
	id, _ := getRecordString(asRecord(payload), "id")
	return id
}

// connection is GitLabDuoWorkflowDirectAccessConnection.
type connection struct {
	token string
	// baseURL is the service endpoint's, "" for GitLab's own socket.
	baseURL         string
	headers         map[string]string
	serviceEndpoint bool
}

// directAccess is requestGitLabDuoWorkflowDirectAccess: the workflow token,
// and the service endpoint when GitLab hands one out instead of its own.
func (r *rest) directAccess(ctx context.Context, definition, rootNamespaceID, projectID string) (connection, error) {
	answer, err := r.do(ctx, http.MethodPost, "/api/v4/ai/duo_workflows/direct_access", DirectAccessBody(definition, rootNamespaceID, projectID))
	if err != nil {
		return connection{}, err
	}
	if !answer.ok() {
		message := fmt.Sprintf("GitLab Duo Workflow direct_access failed with HTTP %d", answer.status)
		if detail := responseErrorMessage(answer); detail != "" {
			message += ": " + detail
		}
		return connection{}, &Error{Message: message, Status: answer.status}
	}
	parsed, ok := answer.json()
	if !ok {
		return connection{}, &Error{Message: "GitLab Duo Workflow direct_access returned no JSON"}
	}
	payload := asRecord(parsed)
	token := workflowToken(payload)
	if !truthy(token) {
		return connection{}, &Error{Message: "GitLab Duo Workflow direct_access did not return credentials"}
	}
	rails := getRecord(payload, "gitlab_rails")
	service := getRecord(payload, "duo_workflow_service")
	var railsToken, serviceBase any
	if rails != nil {
		railsToken, _ = rails.Get("token")
	}
	if service != nil {
		serviceBase, _ = service.Get("base_url")
	}
	out := connection{token: jsString(token), headers: map[string]string{}}
	out.serviceEndpoint = !truthy(railsToken) && truthy(serviceBase)
	if out.serviceEndpoint {
		out.baseURL = serviceBaseURL(jsString(serviceBase))
		if headers := getRecord(service, "headers"); headers != nil {
			for _, name := range headers.Keys() {
				value, _ := headers.Get(name)
				out.headers[name] = jsString(value)
			}
		}
	}
	return out, nil
}

// workflowToken is extractGitLabWorkflowToken's `??` chain.
func workflowToken(payload *cursorwire.JSONObject) any {
	reads := []func() any{
		func() any { return property(getRecord(payload, "gitlab_rails"), "token") },
		func() any { return property(getRecord(payload, "duo_workflow_service"), "token") },
		func() any { return property(payload, "duo_workflow_access_token") },
		func() any { return property(payload, "workflow_token") },
		func() any { return property(payload, "token") },
		func() any { return property(payload, "access_token") },
		func() any { return property(payload, "jwt") },
	}
	for _, read := range reads {
		if value := read(); value != nil {
			return value
		}
	}
	return nil
}

func property(object *cursorwire.JSONObject, key string) any {
	if object == nil {
		return nil
	}
	value, _ := object.Get(key)
	return value
}

// serviceBaseURL is normalizeGitLabDuoWorkflowServiceBaseUrl.
func serviceBaseURL(baseURL string) string {
	trimmed := jsTrim(baseURL)
	if lower := strings.ToLower(trimmed); !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		trimmed = "https://" + trimmed
	}
	return trimTrailingSlashes(trimmed)
}

// createWorkflow is createGitLabDuoWorkflow. The inline flow's goal rides
// the start request, so the created workflow carries an empty one.
func (r *rest) createWorkflow(ctx context.Context, definition, namespaceID, projectID string) (string, error) {
	answer, err := r.do(ctx, http.MethodPost, "/api/v4/ai/duo_workflows/workflows", CreateBody(definition, namespaceID, projectID, ""))
	if err != nil {
		return "", err
	}
	if !answer.ok() {
		return "", &Error{Message: fmt.Sprintf("GitLab Duo Workflow create failed with HTTP %d", answer.status), Status: answer.status}
	}
	parsed, ok := answer.json()
	if !ok {
		return "", &Error{Message: "GitLab Duo Workflow create returned no JSON"}
	}
	payload := asRecord(parsed)
	for _, key := range []string{"id", "workflow_id", "workflowId"} {
		if value := property(payload, key); value != nil {
			return jsString(value), nil
		}
	}
	return "", &Error{Message: fmt.Sprintf("GitLab Duo Workflow create response missing workflow id (HTTP %d)", answer.status)}
}

// stopWorkflow is stopGitLabDuoWorkflow: a best-effort stop the caller's
// cancellation does not cancel.
func (r *rest) stopWorkflow(ctx context.Context, workflowID string) {
	_, _ = r.do(context.WithoutCancel(ctx), http.MethodPatch, "/api/v4/ai/duo_workflows/workflows/"+encodeURIComponent(workflowID), []byte(StopBody))
}

// pinnedModelRef is fetchGitLabDuoWorkflowAvailableModels then
// selectGitLabDuoWorkflowModelRef's pin: the namespace's pinned model ref,
// "" when there is none or the lookup failed.
func (r *rest) pinnedModelRef(ctx context.Context, rootNamespaceID string) string {
	body := jsjson.Object(
		jsjson.Field{Name: "query", Value: jsjson.Marshal(availableModelsQuery)},
		jsjson.Field{Name: "variables", Value: jsjson.Object(jsjson.Field{Name: "rootNamespaceId", Value: jsjson.Marshal(graphQLRootNamespaceID(rootNamespaceID))})},
	)
	answer, err := r.do(ctx, http.MethodPost, graphQLPath, body)
	if err != nil || !answer.ok() {
		return ""
	}
	payload, ok := answer.json()
	if !ok {
		return ""
	}
	models := getRecord(getRecord(asRecord(payload), "data"), "aiChatAvailableModels")
	ref, _ := getRecordString(getRecord(models, "pinnedModel"), "ref")
	return ref
}

// responseErrorMessage is readGitLabDuoWorkflowResponseErrorMessage: the
// body's non-blank message, else its non-blank error.
func responseErrorMessage(answer restAnswer) string {
	parsed, ok := answer.json()
	if !ok {
		return ""
	}
	payload, _ := parsed.(*cursorwire.JSONObject)
	for _, key := range []string{"message", "error"} {
		if value, ok := property(payload, key).(string); ok && jsTrim(value) != "" {
			return value
		}
	}
	return ""
}

// truthy is JavaScript truthiness of a JSON value.
func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v != 0 && !math.IsNaN(v)
	case string:
		return v != ""
	}
	return true
}

// jsString is String(value) of a JSON value.
func jsString(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return jsjson.Number(v)
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item != nil {
				parts[i] = jsString(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}
