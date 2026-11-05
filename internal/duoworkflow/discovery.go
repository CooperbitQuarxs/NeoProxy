// Package duoworkflow is NeoPi's GitLab Duo Agent provider (`gitlab-duo-agent`):
// the namespace and model discovery of pi-catalog discovery/gitlab-duo-workflow.ts
// and the Duo Workflow runtime of pi-ai providers/gitlab-duo-workflow.ts.
package duoworkflow

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

// Provider is NeoPi's provider id, and its API.
const Provider = "gitlab-duo-agent"

const (
	defaultBaseURL = "https://gitlab.com"
	graphQLPath    = "/api/graphql"
	projectsPath   = "/api/v4/projects"
	groupsPath     = "/api/v4/groups"
	// maxGroupPages bounds the top-level group pagination (100 per page).
	maxGroupPages = 50
	// defaultContextWindow is DWS' own global fallback window.
	defaultContextWindow = 200_000
	// bunUserAgent is the runtime discoveryFetch's requests go out under.
	bunUserAgent = "Bun/1.3.14"
)

// contextWindowRules match a model ref to the static window NeoPi ships.
var contextWindowRules = []struct {
	pattern *regexp.Regexp
	window  int
}{
	{regexp.MustCompile(`(?i)claude[_-]?opus`), 1_000_000},
	{regexp.MustCompile(`(?i)claude[_-]?sonnet`), 1_000_000},
	{regexp.MustCompile(`(?i)claude[_-]?haiku`), 200_000},
	{regexp.MustCompile(`(?i)gemini`), 1_000_000},
	{regexp.MustCompile(`(?i)gpt[_-]?5`), 400_000},
}

const aiChatAvailableModelsQuery = `query lsp_aiChatAvailableModels($rootNamespaceId: GroupID!) {
  aiChatAvailableModels(rootNamespaceId: $rootNamespaceId) {
    defaultModel { name ref }
    selectableModels { name ref }
    pinnedModel { name ref }
  }
}`

const projectRootNamespaceQuery = `query omp_gitlabDuoWorkflowProjectRootNamespace($fullPath: ID!) {
  project(fullPath: $fullPath) {
    namespace {
      id
      rootAncestor { id }
    }
  }
}`

// DiscoveryConfig is GitLabDuoWorkflowDiscoveryConfig: the GitLab token, the
// instance, and an explicit namespace or project. The environment's
// GITLAB_DUO_NAMESPACE_ID, GITLAB_DUO_PROJECT_ID and GITLAB_DUO_PROJECT_PATH
// fill what is not set. NeoPi also reads the workspace's git remote; a proxy
// has no client workspace, so that step is not taken.
type DiscoveryConfig struct {
	APIKey      string
	BaseURL     string
	NamespaceID string
	ProjectID   string
	ProjectPath string
	Client      *http.Client
	// UserAgent is the User-Agent the requests carry; "" is Bun's own. The
	// runtime's discovery goes through NeoPi's transport fetch instead.
	UserAgent string
}

// ModelRef is a model GitLab offers a namespace.
type ModelRef struct {
	Name string
	Ref  string
}

// Model is buildGitLabDuoWorkflowModelSpec's row: no reasoning, text input,
// tools, free, a static window and the namespace it was discovered in.
type Model struct {
	ID              string
	Name            string
	BaseURL         string
	ContextWindow   int
	RootNamespaceID string
}

// NamespaceSelection is the namespace a workflow runs in, its path, and the
// project it was resolved from when known.
type NamespaceSelection struct {
	RootNamespaceID string
	NamespacePath   string
	ProjectPath     string
	Source          string
}

type availability struct {
	defaultModel *ModelRef
	selectable   []ModelRef
	pinned       *ModelRef
}

// FetchModels is fetchGitLabDuoWorkflowModels: the namespace with models,
// then its models (pinned, else selectable, else default) as rows. A
// namespace whose models vanish between the calls gives none.
func FetchModels(ctx context.Context, config DiscoveryConfig) ([]Model, error) {
	baseURL := normalizeBaseURL(config.BaseURL)
	selection, ok := selectCandidate(ctx, config, baseURL, false, func(c candidate) (NamespaceSelection, bool) {
		root, ok := normalizeText(c.rootNamespaceID)
		if !ok {
			return NamespaceSelection{}, false
		}
		models := fetchAvailability(ctx, config, baseURL, root)
		if models == nil || len(resolveModelRefs(models)) == 0 {
			return NamespaceSelection{}, false
		}
		path, _ := normalizeText(c.namespacePath)
		return NamespaceSelection{RootNamespaceID: root, NamespacePath: path, Source: c.source}, true
	})
	if !ok {
		return nil, errors.New("Unable to find a GitLab Duo Workflow namespace with available models. Set GITLAB_DUO_NAMESPACE_ID to a root namespace with Duo model access.")
	}
	models := fetchAvailability(ctx, config, baseURL, selection.RootNamespaceID)
	if models == nil {
		return nil, nil
	}
	refs := resolveModelRefs(models)
	if len(refs) == 0 {
		return nil, nil
	}
	rows := make([]Model, len(refs))
	for i, ref := range refs {
		rows[i] = Model{ID: ref.Ref, Name: ref.Name, BaseURL: baseURL, ContextWindow: contextWindow(ref.Ref), RootNamespaceID: selection.RootNamespaceID}
	}
	return rows, nil
}

// DiscoverRuntimeNamespace is discoverGitLabDuoWorkflowRuntimeNamespace: the
// first namespace candidate, an explicit namespace enriched from its group.
func DiscoverRuntimeNamespace(ctx context.Context, config DiscoveryConfig) (NamespaceSelection, error) {
	baseURL := normalizeBaseURL(config.BaseURL)
	selection, ok := selectCandidate(ctx, config, baseURL, true, func(c candidate) (NamespaceSelection, bool) {
		root, ok := normalizeText(c.rootNamespaceID)
		if !ok {
			return NamespaceSelection{}, false
		}
		path, _ := normalizeText(c.namespacePath)
		project, _ := normalizeText(c.projectPath)
		return NamespaceSelection{RootNamespaceID: root, NamespacePath: path, ProjectPath: project, Source: c.source}, true
	})
	if !ok {
		return NamespaceSelection{}, errors.New("Unable to find a GitLab Duo Workflow namespace. Set GITLAB_DUO_NAMESPACE_ID to a root namespace or GITLAB_DUO_PROJECT_ID to a GitLab project.")
	}
	return selection, nil
}

type candidate struct {
	rootNamespaceID string
	namespacePath   string
	projectPath     string
	source          string
}

// selectCandidate is selectGitLabDuoWorkflowCandidate: an explicit namespace,
// then an explicit project's root namespace, then the token's top-level
// groups (Duo-enabled first); the first candidate resolve accepts wins.
func selectCandidate(ctx context.Context, config DiscoveryConfig, baseURL string, enrichOverride bool, resolve func(candidate) (NamespaceSelection, bool)) (NamespaceSelection, bool) {
	if namespaceID, ok := firstText(config.NamespaceID, os.Getenv("GITLAB_DUO_NAMESPACE_ID")); ok {
		override := candidate{rootNamespaceID: namespaceID, source: "override"}
		if enrichOverride {
			if enriched, found := fetchNamespaceOverrideCandidate(ctx, config, baseURL, namespaceID); found {
				override = enriched
			}
		}
		if selected, ok := resolve(override); ok {
			return selected, true
		}
	}
	if projectID, ok := firstText(config.ProjectID, config.ProjectPath, os.Getenv("GITLAB_DUO_PROJECT_ID"), os.Getenv("GITLAB_DUO_PROJECT_PATH")); ok {
		if root, found := fetchProjectRootNamespace(ctx, config, baseURL, projectID); found {
			project := candidate{rootNamespaceID: root, source: "project"}
			// Only a full path is meaningful as a runtime project scope.
			if strings.Contains(projectID, "/") {
				project.projectPath = projectID
			}
			if selected, ok := resolve(project); ok {
				return selected, true
			}
		}
	}
	for _, group := range fetchTopLevelGroupCandidates(ctx, config, baseURL) {
		if selected, ok := resolve(group); ok {
			return selected, true
		}
	}
	return NamespaceSelection{}, false
}

func fetchAvailability(ctx context.Context, config DiscoveryConfig, baseURL, rootNamespaceID string) *availability {
	payload, ok := postGraphQL(ctx, config, baseURL, aiChatAvailableModelsQuery, "rootNamespaceId", graphQLRootNamespaceID(rootNamespaceID))
	if !ok {
		return nil
	}
	raw := recordField(payload, "data").Get("aiChatAvailableModels")
	if !raw.Exists() || raw.Type == gjson.Null {
		return nil
	}
	return parseAvailability(raw)
}

func fetchNamespaceOverrideCandidate(ctx context.Context, config DiscoveryConfig, baseURL, namespaceID string) (candidate, bool) {
	rest, ok := restNamespaceID(namespaceID)
	if !ok {
		return candidate{}, false
	}
	payload, ok := getJSON(ctx, config, baseURL+groupsPath+"/"+encodeURIComponent(rest))
	if !ok {
		return candidate{}, false
	}
	root, found := extractRootNamespaceID(payload)
	if !found {
		root = namespaceID
	}
	path, _ := extractNamespacePath(payload)
	return candidate{rootNamespaceID: root, namespacePath: path, source: "override"}, true
}

// fetchProjectRootNamespace resolves a project's root namespace from its REST
// payload, else by GraphQL rootAncestor on its full path.
func fetchProjectRootNamespace(ctx context.Context, config DiscoveryConfig, baseURL, projectIDOrPath string) (string, bool) {
	fullPath, hasPath := "", false
	if payload, ok := getJSON(ctx, config, baseURL+projectsPath+"/"+encodeURIComponent(projectIDOrPath)); ok {
		if root, found := extractExplicitRootNamespaceID(payload); found {
			return root, true
		}
		fullPath, hasPath = extractProjectFullPath(payload)
	}
	if !hasPath && strings.Contains(projectIDOrPath, "/") {
		fullPath, hasPath = projectIDOrPath, true
	}
	if !hasPath {
		return "", false
	}
	payload, ok := postGraphQL(ctx, config, baseURL, projectRootNamespaceQuery, "fullPath", fullPath)
	if !ok {
		return "", false
	}
	return extractExplicitRootNamespaceID(recordField(recordField(payload, "data"), "project"))
}

// fetchTopLevelGroupCandidates follows GitLab's x-next-page pages of
// top-level groups, keeping NeoPi's order with Duo-enabled groups first.
func fetchTopLevelGroupCandidates(ctx context.Context, config DiscoveryConfig, baseURL string) []candidate {
	type ranked struct {
		candidate
		preferred bool
	}
	var groups []ranked
	nextPage := "1"
	for page := 0; page < maxGroupPages && nextPage != ""; page++ {
		url := baseURL + groupsPath + "?top_level_only=true&per_page=100&order_by=name&sort=asc&page=" + formEscape(nextPage)
		response, ok := do(ctx, config, http.MethodGet, url, nil)
		if !ok {
			break
		}
		if !response.ok || !response.body.IsArray() {
			break
		}
		for _, group := range response.body.Array() {
			root, found := extractRootNamespaceID(group)
			if !found {
				continue
			}
			path, _ := extractNamespacePath(group)
			groups = append(groups, ranked{candidate{rootNamespaceID: root, namespacePath: path, source: "group"}, hasDuoFeatureFlag(group)})
		}
		nextPage = strings.TrimSpace(response.nextPage)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].preferred && !groups[j].preferred })
	out := make([]candidate, len(groups))
	for i, group := range groups {
		out[i] = group.candidate
	}
	return out
}

func postGraphQL(ctx context.Context, config DiscoveryConfig, baseURL, query, variable, value string) (gjson.Result, bool) {
	body := jsjson.Object(
		jsjson.Field{Name: "query", Value: jsjson.Marshal(query)},
		jsjson.Field{Name: "variables", Value: jsjson.Object(jsjson.Field{Name: variable, Value: jsjson.Marshal(value)})},
	)
	response, ok := do(ctx, config, http.MethodPost, baseURL+graphQLPath, body)
	if !ok || !response.ok || !response.valid {
		return gjson.Result{}, false
	}
	return response.body, true
}

func getJSON(ctx context.Context, config DiscoveryConfig, url string) (gjson.Result, bool) {
	response, ok := do(ctx, config, http.MethodGet, url, nil)
	if !ok || !response.ok || !response.valid {
		return gjson.Result{}, false
	}
	return response.body, true
}

type answer struct {
	ok       bool
	valid    bool
	body     gjson.Result
	nextPage string
}

// do sends one discovery request with buildGitLabJsonHeaders as Bun's fetch
// puts it on the wire; a transport failure reports false.
func do(ctx context.Context, config DiscoveryConfig, method, url string, body []byte) (answer, bool) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return answer{}, false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/json")
	userAgent := config.UserAgent
	if userAgent == "" {
		userAgent = bunUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return answer{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	result := answer{ok: resp.StatusCode >= 200 && resp.StatusCode < 300, nextPage: resp.Header.Get("X-Next-Page")}
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return result, true
	}
	defer func() { _ = decoded.Close() }()
	data, err := io.ReadAll(decoded)
	if err == nil && gjson.ValidBytes(data) {
		result.valid, result.body = true, gjson.ParseBytes(data)
	}
	return result, true
}

func parseAvailability(value gjson.Result) *availability {
	if !value.IsObject() {
		return nil
	}
	parsed := &availability{defaultModel: parseModelRef(value.Get("defaultModel")), pinned: parseModelRef(value.Get("pinnedModel"))}
	if selectable := value.Get("selectableModels"); selectable.IsArray() {
		for _, item := range selectable.Array() {
			if ref := parseModelRef(item); ref != nil {
				parsed.selectable = append(parsed.selectable, *ref)
			}
		}
	}
	return parsed
}

// parseModelRef keeps a model with a non-blank string ref, named by its ref
// when its name is missing or blank.
func parseModelRef(value gjson.Result) *ModelRef {
	if !value.IsObject() {
		return nil
	}
	ref, ok := stringText(value.Get("ref"))
	if !ok {
		return nil
	}
	name, ok := stringText(value.Get("name"))
	if !ok {
		name = ref
	}
	return &ModelRef{Name: name, Ref: ref}
}

func resolveModelRefs(models *availability) []ModelRef {
	switch {
	case models.pinned != nil:
		return []ModelRef{*models.pinned}
	case len(models.selectable) > 0:
		return models.selectable
	case models.defaultModel != nil:
		return []ModelRef{*models.defaultModel}
	}
	return nil
}

func contextWindow(ref string) int {
	for _, rule := range contextWindowRules {
		if rule.pattern.MatchString(ref) {
			return rule.window
		}
	}
	return defaultContextWindow
}

// rootRecord is the first of a payload's root namespace objects.
func rootRecord(value gjson.Result) (gjson.Result, bool) {
	for _, key := range []string{"root_namespace", "rootNamespace", "root_ancestor", "rootAncestor"} {
		if record := value.Get(key); record.IsObject() {
			return record, true
		}
	}
	return gjson.Result{}, false
}

func idOrPath(value gjson.Result) (string, bool) {
	return firstIdentifier(value.Get("id"), value.Get("full_path"), value.Get("fullPath"))
}

func extractExplicitRootNamespaceID(value gjson.Result) (string, bool) {
	if !value.IsObject() {
		return "", false
	}
	if direct, ok := firstIdentifier(value.Get("root_namespace_id"), value.Get("rootNamespaceId")); ok {
		return direct, true
	}
	if root, ok := rootRecord(value); ok {
		return idOrPath(root)
	}
	if namespace := value.Get("namespace"); namespace.IsObject() {
		return extractExplicitRootNamespaceID(namespace)
	}
	return "", false
}

func extractRootNamespaceID(value gjson.Result) (string, bool) {
	if !value.IsObject() {
		return "", false
	}
	if direct, ok := firstIdentifier(value.Get("root_namespace_id"), value.Get("rootNamespaceId")); ok {
		return direct, true
	}
	if root, ok := rootRecord(value); ok {
		if nested, ok := idOrPath(root); ok {
			return nested, true
		}
	}
	if namespace := value.Get("namespace"); namespace.IsObject() {
		if root, ok := extractRootNamespaceID(namespace); ok {
			return root, true
		}
		return idOrPath(namespace)
	}
	return idOrPath(value)
}

func extractNamespacePath(value gjson.Result) (string, bool) {
	if !value.IsObject() {
		return "", false
	}
	return firstIdentifier(value.Get("full_path"), value.Get("fullPath"), value.Get("path"))
}

func extractProjectFullPath(value gjson.Result) (string, bool) {
	if !value.IsObject() {
		return "", false
	}
	return firstIdentifier(value.Get("path_with_namespace"), value.Get("fullPath"))
}

func hasDuoFeatureFlag(value gjson.Result) bool {
	return value.IsObject() && (value.Get("duo_features_enabled").Type == gjson.True || value.Get("duo_core_features_enabled").Type == gjson.True)
}

// recordField is getRecord(value, key): the field when it is an object.
func recordField(value gjson.Result, key string) gjson.Result {
	if field := value.Get(key); value.IsObject() && field.IsObject() {
		return field
	}
	return gjson.Result{}
}

// identifier is normalizeIdentifier: a string or number, trimmed, non-blank.
func identifier(value gjson.Result) (string, bool) {
	switch value.Type {
	case gjson.String:
		return normalizeText(value.Str)
	case gjson.Number:
		return normalizeText(jsjson.Number(value.Num))
	}
	return "", false
}

// stringText is normalizeIdentifier over a string-only field.
func stringText(value gjson.Result) (string, bool) {
	if value.Type != gjson.String {
		return "", false
	}
	return normalizeText(value.Str)
}

func firstIdentifier(values ...gjson.Result) (string, bool) {
	for _, value := range values {
		if text, ok := identifier(value); ok {
			return text, true
		}
	}
	return "", false
}

func normalizeText(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	return trimmed, trimmed != ""
}

func firstText(values ...string) (string, bool) {
	for _, value := range values {
		if text, ok := normalizeText(value); ok {
			return text, true
		}
	}
	return "", false
}

var (
	restGroupID = regexp.MustCompile(`^gid://gitlab/(?:Group|Namespace)/(\d+)$`)
	digits      = regexp.MustCompile(`^\d+$`)
)

func restNamespaceID(namespaceID string) (string, bool) {
	if match := restGroupID.FindStringSubmatch(namespaceID); match != nil {
		return match[1], true
	}
	return namespaceID, digits.MatchString(namespaceID)
}

func graphQLRootNamespaceID(rootNamespaceID string) string {
	if digits.MatchString(rootNamespaceID) {
		return "gid://gitlab/Group/" + rootNamespaceID
	}
	return rootNamespaceID
}

func normalizeBaseURL(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return defaultBaseURL
	}
	if trimmed := strings.TrimRight(raw, "/"); trimmed != "" {
		return trimmed
	}
	return defaultBaseURL
}

// encodeURIComponent escapes all but A-Z a-z 0-9 - _ . ! ~ * ' ( ).
func encodeURIComponent(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || strings.IndexByte("-_.!~*'()", b) >= 0 {
			out.WriteByte(b)
			continue
		}
		out.WriteString("%" + strings.ToUpper(string("0123456789abcdef"[b>>4])) + strings.ToUpper(string("0123456789abcdef"[b&15])))
	}
	return out.String()
}

// formEscape is URLSearchParams' escaping of a query value.
func formEscape(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		switch {
		case b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || strings.IndexByte("*-._", b) >= 0:
			out.WriteByte(b)
		case b == ' ':
			out.WriteByte('+')
		default:
			out.WriteString("%" + strings.ToUpper(string("0123456789abcdef"[b>>4])) + strings.ToUpper(string("0123456789abcdef"[b&15])))
		}
	}
	return out.String()
}
