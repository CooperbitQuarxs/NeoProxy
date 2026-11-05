package duoworkflow

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopidialect"
	"github.com/tidwall/gjson"
)

// The Duo Workflow runtime of pi-ai providers/gitlab-duo-workflow.ts
// (streamGitLabDuoWorkflow). Each turn resumes the session's live workflow
// when it can (a tool result for its pending action, or a paused turn) and
// otherwise seeds a fresh workflow whose goal replays the conversation.
//
// NeoPi's deadlines are not ported: this proxy sets no timeout after a
// connection is made, so the 90 s socket idle restart and the 30 s REST
// budget are gone, and a silent socket waits until the client goes away.

const (
	maxStepLimitRestarts   = 4
	maxGenericErrorRetries = 1
	maxStallRestarts       = 2
	maxSocketAttempts      = 12
	goalSoftOverflowBytes  = 1_048_576
	goalHardOverflowBytes  = 2_000_000
	// stallErrorMessage is GITLAB_DUO_WORKFLOW_STALL_ERROR_MESSAGE.
	stallErrorMessage = "GitLab Duo Agent stopped making progress (the workflow's visible history did not advance after multiple restarts)."
	// maxSessions bounds the live workflow sessions; NeoPi drops one with its
	// agent session, and this proxy has no such signal, so the least recently
	// used session is closed (its workflow stopped) past the bound.
	maxSessions = 256
)

// Socket loop results (GitLabDuoWorkflowSocketResult and the per-message
// GitLabDuoWorkflowMessageResult).
const (
	resultContinue  = "continue"
	resultClosed    = "closed"
	resultTerminal  = "terminal"
	resultApproval  = "approval"
	resultAction    = "action"
	resultPause     = "pause"
	resultStepLimit = "step_limit"
	resultRetryable = "retryable_error"
	resultStalled   = "stalled"
)

// Error is a workflow failure; Status is the HTTP status it carries, 0 for
// none.
type Error struct {
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Message }

// Event is one AssistantMessageEvent of the turn's stream, after NeoPi's
// leaked-thinking healing, which streamSimple applies to this provider.
type Event = neopidialect.StreamEvent

// Output is the AssistantMessage a turn produced.
type Output struct {
	Content    []*cursorwire.OutBlock
	Usage      cursorwire.Usage
	StopReason string
	// StopDetails is "pause_turn" when the workflow paused mid-run.
	StopDetails  string
	ErrorMessage string
	// ErrorStatus is the HTTP status the failure carried, 0 for none.
	ErrorStatus int
}

// Options are the namespace and tool settings of GitLabDuoWorkflowOptions.
// Unset namespace fields fall back to GITLAB_DUO_NAMESPACE_ID,
// GITLAB_DUO_PROJECT_ID and GITLAB_DUO_PROJECT_PATH; the workflow definition
// is GITLAB_DUO_WORKFLOW_DEFINITION's, else ambient.
type Options struct {
	RootNamespaceID string
	NamespaceID     string
	ProjectID       string
	ProjectPath     string
	// ToolChoiceNone is tool_choice "none": the tools are not advertised.
	ToolChoiceNone bool
}

// Request is one turn.
type Request struct {
	APIKey  string
	BaseURL string
	ModelID string
	// Scope names whose workflows these are: the account and the caller
	// behind the request. NeoPi keeps a session's workflow in that agent
	// session's own state; a proxy shares one runtime among callers and
	// accounts, so a session id resumes a workflow only within its scope.
	Scope     string
	SessionID string
	Context   *cursorwire.Context
	Options   Options
	Client    *http.Client
	// Finalize applies the configured payload rules to a start request; nil
	// leaves it as built.
	Finalize func([]byte) []byte
	// Sink receives the turn's events.
	Sink func(Event)
	// Dial opens the turn's workflow sockets.
	Dial Dialer
}

// Runtime holds the live workflow sessions and the per-account namespace
// state across turns.
type Runtime struct {
	mu       sync.Mutex
	sessions map[string]*list.Element
	lru      *list.List
	accounts map[string]*accountState
}

// NewRuntime returns an empty runtime.
func NewRuntime() *Runtime {
	return &Runtime{sessions: map[string]*list.Element{}, lru: list.New(), accounts: map[string]*accountState{}}
}

type sessionEntry struct {
	key     string
	session *session
}

// session is GitLabDuoWorkflowProviderSessionState. Its lock is the event
// loop: a turn holds it except while it awaits GitLab, and socket handlers
// take it to run. turn admits one turn at a time, as an agent session runs
// its turns in order; it stays held across the turn's awaits.
type session struct {
	turn   sync.Mutex
	mu     sync.Mutex
	active *activeSession
}

// close is the session state's close(): stop the workflow, close its socket.
func (s *session) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		if s.active.stop != nil {
			s.active.stop()
		}
		s.active.ws.close()
		s.active = nil
	}
}

// activeSession is GitLabDuoWorkflowActiveSession.
type activeSession struct {
	workflowID string
	start      *StartRequest
	approval   bool
	ws         *socket
	stop       func()
	pending    []*action
	byKey      map[string]string
	signatures map[signature]bool
	paused     bool
	pauseQueue []string
	// lastBoundaryLength is the checkpoint length at the last tool-call
	// boundary; equal lengths at consecutive boundaries are a stall.
	lastBoundaryLength int
	hasLastBoundary    bool
}

// accountState is GitLabDuoWorkflowAccountState.
type accountState struct {
	namespace       *NamespaceSelection
	settingsEnsured bool
}

func (rt *Runtime) session(baseURL, scope, modelID, sessionID string) *session {
	key := Provider + ":" + baseURL + "\x00" + scope + "\x00" + modelID + "\x00" + sessionID
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if element, ok := rt.sessions[key]; ok {
		rt.lru.MoveToFront(element)
		return element.Value.(*sessionEntry).session
	}
	created := &session{}
	rt.sessions[key] = rt.lru.PushFront(&sessionEntry{key: key, session: created})
	for rt.lru.Len() > maxSessions {
		oldest := rt.lru.Back()
		entry := rt.lru.Remove(oldest).(*sessionEntry)
		delete(rt.sessions, entry.key)
		go entry.session.close()
	}
	return created
}

// account is the account state keyed by a credential fingerprint and the
// instance; the proxy has no client workspace, so no cwd enters the key.
func (rt *Runtime) account(apiKey, baseURL string) *accountState {
	sum := sha256.Sum256([]byte(apiKey))
	key := hex.EncodeToString(sum[:8]) + "\x00" + baseURL
	if rt.accounts[key] == nil {
		rt.accounts[key] = &accountState{}
	}
	return rt.accounts[key]
}

func (rt *Runtime) cachedNamespace(apiKey, baseURL string) *NamespaceSelection {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.account(apiKey, baseURL).namespace
}

func (rt *Runtime) setCachedNamespace(apiKey, baseURL string, selection *NamespaceSelection) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.account(apiKey, baseURL).namespace = selection
}

func (rt *Runtime) settingsEnsured(apiKey, baseURL string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.account(apiKey, baseURL).settingsEnsured
}

func (rt *Runtime) markSettingsEnsured(apiKey, baseURL string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.account(apiKey, baseURL).settingsEnsured = true
}

// Stream runs one turn: its events go to the request's sink, and the
// finished message is returned.
func (rt *Runtime) Stream(ctx context.Context, req *Request) *Output {
	baseURL := trimTrailingSlashes(req.BaseURL)
	sess := rt.session(baseURL, req.Scope, req.ModelID, req.SessionID)
	sess.turn.Lock()
	defer sess.turn.Unlock()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	t := newTurn(req.Sink)
	t.session = sess
	t.push(Event{Type: "start"})
	c := &call{rt: rt, ctx: ctx, req: req, baseURL: baseURL, sess: sess, t: t,
		rest: &rest{client: req.Client, baseURL: baseURL, apiKey: req.APIKey}}
	if err := c.run(); err != nil && !t.done {
		t.output.StopReason = "error"
		t.output.ErrorMessage = err.Error()
		if t.goalOverflow != "" {
			t.output.ErrorMessage = t.goalOverflow
		}
		var workflowErr *Error
		if errors.As(err, &workflowErr) {
			t.output.ErrorStatus = workflowErr.Status
		}
		t.push(Event{Type: "error"})
	}
	out := t.output.snapshot()
	if healed := t.projector.Content(); healed != nil {
		out.Content = (&Output{Content: healed}).snapshot().Content
	}
	return out
}

func (o *Output) snapshot() *Output {
	out := *o
	out.Content = append([]*cursorwire.OutBlock(nil), o.Content...)
	for i, block := range out.Content {
		copied := *block
		out.Content[i] = &copied
	}
	return &out
}

// call is one runGitLabDuoWorkflow.
type call struct {
	rt      *Runtime
	ctx     context.Context
	req     *Request
	baseURL string
	sess    *session
	t       *turn
	rest    *rest
}

// await runs f with the session's lock released, as an await yields
// NeoPi's event loop to the socket handlers.
func (c *call) await(f func()) {
	c.sess.mu.Unlock()
	defer c.sess.mu.Lock()
	f()
}

func (c *call) stop(workflowID string) {
	c.await(func() { c.rest.stopWorkflow(c.ctx, workflowID) })
}

func (c *call) run() error {
	if c.req.APIKey == "" {
		return &Error{Message: "No API key for provider: " + Provider, Status: http.StatusUnauthorized}
	}
	messages := c.req.Context.Messages
	pending := c.sess.active
	if pending != nil {
		c.t.byKey, c.t.signatures = pending.byKey, pending.signatures
	}
	var pendingActions []*action
	var batch []resolvedAction
	resolved := false
	if pending != nil {
		pendingActions = pending.pending
		if len(pendingActions) > 0 {
			batch, resolved = resolveActionBatch(messages, pendingActions)
		}
	}
	// A user turn after the tool results is a steer the live socket has no
	// way to carry, so the workflow is abandoned for a fresh one.
	steered := resolved && steerAfterBatch(messages, batch)
	if pending != nil && resolved && !steered {
		responses := make([]string, len(batch))
		for i, entry := range batch {
			responses[i] = c.finalizedFrame("actionResponse", actionResponse(entry.requestID, entry.result))
		}
		pending.pending = nil
		result, err := c.resume(pending.workflowID, func() (string, error) {
			return c.socket(pending.ws, "", responses, nil)
		})
		if err != nil {
			return err
		}
		if result != resultStalled {
			return nil
		}
	}
	if active := c.sess.active; active != nil && active.paused {
		replay := active.pauseQueue
		active.paused, active.pauseQueue = false, nil
		result, err := c.resume(active.workflowID, func() (string, error) {
			return c.socket(active.ws, "", nil, replay)
		})
		if err != nil {
			return err
		}
		if result != resultStalled {
			return nil
		}
	}
	if pending != nil && (steered || (len(pendingActions) > 0 && !resolved)) {
		pending.pending = nil
		pending.ws.close()
		c.sess.active = nil
		c.stop(pending.workflowID)
	}
	return c.fresh()
}

// setup is GitLabDuoWorkflowNamespaceSetup.
type setup struct {
	restNamespaceID   string
	createNamespaceID string
	restProjectID     string
	webSocketProject  string
	connection        connection
	workflowID        string
	selectedModel     string
	start             *StartRequest
}

func (c *call) fresh() error {
	opts := c.req.Options
	definition := firstNonEmpty(os.Getenv("GITLAB_DUO_WORKFLOW_DEFINITION"))
	if definition == "" {
		definition = WorkflowDefinition
	}
	envNamespace := os.Getenv("GITLAB_DUO_NAMESPACE_ID")
	envProjectID := os.Getenv("GITLAB_DUO_PROJECT_ID")
	envProjectPath := os.Getenv("GITLAB_DUO_PROJECT_PATH")
	explicit := firstNonEmpty(opts.RootNamespaceID, opts.NamespaceID, envNamespace, opts.ProjectID, opts.ProjectPath, envProjectID, envProjectPath) != ""
	configuredProjectPath := firstNonEmpty(opts.ProjectPath, envProjectPath)
	configuredProjectID := firstNonEmpty(opts.ProjectID, envProjectID)

	resolveNamespace := func() (*NamespaceSelection, error) {
		var selection NamespaceSelection
		var err error
		c.await(func() {
			selection, err = DiscoverRuntimeNamespace(c.ctx, DiscoveryConfig{
				APIKey:      c.req.APIKey,
				BaseURL:     c.baseURL,
				NamespaceID: firstNonEmpty(opts.RootNamespaceID, opts.NamespaceID, envNamespace),
				ProjectID:   firstNonEmpty(opts.ProjectID, opts.ProjectPath, envProjectID, envProjectPath),
				Client:      c.req.Client,
				UserAgent:   userAgent(),
			})
		})
		if err != nil {
			return nil, &Error{Message: "GitLab Duo Workflow runtime namespace resolution failed: " + err.Error()}
		}
		return &selection, nil
	}

	setupFor := func(selection *NamespaceSelection) (*setup, error) {
		root := selection.RootNamespaceID
		s := &setup{restNamespaceID: RestNamespaceID(root)}
		s.createNamespaceID = firstDefined(selection.NamespacePath, s.restNamespaceID)
		if !c.rt.settingsEnsured(c.req.APIKey, c.baseURL) {
			var definitive bool
			c.await(func() { definitive = c.rest.ensureSettings(c.ctx, s.restNamespaceID) })
			if definitive {
				c.rt.markSettingsEnsured(c.req.APIKey, c.baseURL)
			}
		}
		var discovered *project
		if configuredProjectPath == "" && configuredProjectID == "" {
			if selection.ProjectPath != "" {
				discovered = &project{path: selection.ProjectPath}
			} else {
				c.await(func() { discovered = c.rest.discoverProject(c.ctx, s.restNamespaceID) })
			}
		}
		// A configured project id carrying a slash is a full path.
		numericProjectID, pathProjectID := configuredProjectID, ""
		if strings.Contains(configuredProjectID, "/") {
			numericProjectID, pathProjectID = "", configuredProjectID
		}
		var discoveredID, discoveredPath string
		if discovered != nil {
			discoveredID, discoveredPath = discovered.id, discovered.path
		}
		projectPath := firstDefined(configuredProjectPath, pathProjectID, discoveredPath)
		projectID := firstDefined(numericProjectID, discoveredID)
		s.restProjectID = firstDefined(configuredProjectPath, configuredProjectID, discoveredPath)
		s.webSocketProject = projectID
		if projectID == "" && projectPath != "" {
			c.await(func() { s.webSocketProject = c.rest.numericProjectID(c.ctx, projectPath) })
		}
		var err error
		c.await(func() { s.connection, err = c.rest.directAccess(c.ctx, definition, root, s.restProjectID) })
		if err != nil {
			return nil, err
		}
		c.await(func() {
			s.workflowID, err = c.rest.createWorkflow(c.ctx, definition, s.createNamespaceID, s.restProjectID)
		})
		if err != nil {
			return nil, err
		}
		var pinned string
		c.await(func() { pinned = c.rest.pinnedModelRef(c.ctx, root) })
		s.selectedModel = firstDefined(pinned, c.req.ModelID)
		tools := c.req.Context.Tools
		if opts.ToolChoiceNone {
			tools = nil
		}
		s.start, err = BuildStartRequest(s.workflowID, c.req.ModelID, c.req.Context, tools, pinned, StartMetadata{
			ProjectID: s.webSocketProject, NamespaceID: s.restNamespaceID, RootNamespaceID: s.restNamespaceID, Definition: definition,
		})
		if err != nil {
			return nil, err
		}
		return s, nil
	}

	var s *setup
	var err error
	cached := (*NamespaceSelection)(nil)
	if !explicit {
		cached = c.rt.cachedNamespace(c.req.APIKey, c.baseURL)
	}
	if cached != nil {
		if s, err = setupFor(cached); err != nil {
			// The cached namespace stopped working: discover once more.
			c.rt.setCachedNamespace(c.req.APIKey, c.baseURL, nil)
			rediscovered, errResolve := resolveNamespace()
			if errResolve != nil {
				return errResolve
			}
			if s, err = setupFor(rediscovered); err != nil {
				return err
			}
			c.rt.setCachedNamespace(c.req.APIKey, c.baseURL, rediscovered)
		}
	} else {
		selection, errResolve := resolveNamespace()
		if errResolve != nil {
			return errResolve
		}
		if s, err = setupFor(selection); err != nil {
			return err
		}
		if !explicit {
			c.rt.setCachedNamespace(c.req.APIKey, c.baseURL, selection)
		}
	}
	return c.loop(s, definition)
}

// loop opens the workflow socket and restarts it as NeoPi does: approval
// on the same workflow, and a fresh workflow after the step limit, a stall
// or the generic service error.
func (c *call) loop(s *setup, definition string) error {
	t := c.t
	workflowID, start, approval := s.workflowID, s.start, false
	goalBytes := len(start.Goal)
	if goalBytes >= goalHardOverflowBytes {
		if !t.done {
			t.output.StopReason = "error"
			t.output.ErrorMessage = goalOverflowMessage(goalBytes)
			t.push(Event{Type: "error"})
		}
		c.sess.active = nil
		c.stop(workflowID)
		return nil
	}
	if goalBytes >= goalSoftOverflowBytes {
		t.goalOverflow = goalOverflowMessage(goalBytes)
	}
	restart := func() error {
		c.stop(workflowID)
		var created string
		var err error
		c.await(func() { created, err = c.rest.createWorkflow(c.ctx, definition, s.createNamespaceID, s.restProjectID) })
		if err != nil {
			return err
		}
		workflowID = created
		next := *start
		next.WorkflowID = workflowID
		start = &next
		return nil
	}
	lastResult := resultClosed
	stepLimitRestarts, genericErrorRetries, stallRestarts := 0, 0, 0
	var loopErr error
	for attempt := 0; attempt < maxSocketAttempts; attempt++ {
		ws := c.open(s, definition)
		stopRest, stopID := c.rest, workflowID
		c.sess.active = &activeSession{workflowID: workflowID, start: start, approval: approval, ws: ws,
			stop: func() { go stopRest.stopWorkflow(context.Background(), stopID) }}
		result, err := c.socket(ws, c.startFrame(start, approval), nil, nil)
		if err != nil {
			loopErr = err
			break
		}
		lastResult = result
		if result == resultApproval {
			approval = true
			t.lastApprovalStatus = ""
			continue
		}
		if result == resultStepLimit && stepLimitRestarts < maxStepLimitRestarts {
			stepLimitRestarts++
			t.stepLimitRequested = false
			if loopErr = restart(); loopErr != nil {
				break
			}
			continue
		}
		if result == resultStalled && stallRestarts < maxStallRestarts {
			stallRestarts++
			t.stalledRequested = false
			if loopErr = restart(); loopErr != nil {
				break
			}
			continue
		}
		if result == resultRetryable && genericErrorRetries < maxGenericErrorRetries {
			genericErrorRetries++
			t.retryableRequested = false
			t.output.ErrorMessage = ""
			if loopErr = restart(); loopErr != nil {
				break
			}
			continue
		}
		if result == resultRetryable && !t.done {
			t.output.StopReason = "error"
			if t.goalOverflow != "" {
				t.output.ErrorMessage = t.goalOverflow
			}
			t.push(Event{Type: "error"})
		}
		if result == resultStalled && !t.done {
			t.output.StopReason = "error"
			t.output.ErrorMessage = firstDefined(t.goalOverflow, t.output.ErrorMessage, stallErrorMessage)
			t.push(Event{Type: "error"})
		}
		break
	}
	if loopErr == nil {
		c.finalizeResume(lastResult)
	}
	// A failed, aborted, dropped or stalled run leaves the workflow running
	// server-side with nothing to resume it: stop it.
	if c.ctx.Err() != nil || loopErr != nil || lastResult == resultClosed || lastResult == resultStalled {
		c.sess.active = nil
		c.stop(workflowID)
	}
	return loopErr
}

// open is openGitLabDuoWorkflowSocket: the service endpoint's socket when
// direct_access handed one out, else GitLab's.
func (c *call) open(s *setup, definition string) *socket {
	socketBase := c.baseURL
	if s.connection.baseURL != "" {
		socketBase = s.connection.baseURL
	}
	options := SocketOptions{
		ProjectID:               s.webSocketProject,
		NamespaceID:             s.restNamespaceID,
		RootNamespaceID:         s.restNamespaceID,
		SelectedModelIdentifier: s.selectedModel,
		WorkflowDefinition:      definition,
		ServiceEndpoint:         s.connection.serviceEndpoint,
	}
	ws := &socket{loop: &c.sess.mu}
	ws.conn = c.req.Dial(WebSocketURL(socketBase, options), WebSocketHeaders(s.connection.token, c.baseURL, options, s.connection.headers), ws.deliver)
	return ws
}

// startFrame is `{startRequest}` with the configured payload rules applied
// to the start request.
func (c *call) startFrame(start *StartRequest, approval bool) string {
	return c.finalizedFrame("startRequest", start.JSON(approval))
}

// finalizedFrame applies the configured payload rules once to the business
// payload a client frame `{key: payload}` carries: a start request, or a
// tool's answer resuming the workflow.
func (c *call) finalizedFrame(key string, frame json.RawMessage) string {
	if c.req.Finalize == nil {
		return string(frame)
	}
	payload := gjson.GetBytes(frame, key)
	if !payload.IsObject() {
		return string(frame)
	}
	finalized := c.req.Finalize([]byte(payload.Raw))
	return string(jsjson.Object(jsjson.Field{Name: key, Value: json.RawMessage(finalized)}))
}

// resume is resumeGitLabDuoWorkflowSocket: a run on the preserved socket,
// finalized as the fresh loop finalizes; a failure or a stall drops the
// session and stops its workflow.
func (c *call) resume(workflowID string, run func() (string, error)) (string, error) {
	result, err := run()
	if err != nil {
		c.sess.active = nil
		c.stop(workflowID)
		return "", err
	}
	if result == resultStalled {
		c.sess.active = nil
		c.stop(workflowID)
		return result, nil
	}
	c.finalizeResume(result)
	if result == resultClosed {
		c.stop(workflowID)
	}
	return result, nil
}

// finalizeResume is finalizeGitLabDuoWorkflowResumeResult: an action or a
// pause keeps the session; anything else drops it and ends the turn.
func (c *call) finalizeResume(result string) {
	if result == resultAction || result == resultPause {
		return
	}
	c.sess.active = nil
	if result != resultTerminal && !c.t.done {
		c.t.finish("stop")
	}
}

// socket is runGitLabDuoWorkflowSocket: install the handlers, then send the
// start request on open, answer the pending action, or replay a paused
// turn's frames, and wait for the run to settle. A socket that closed
// before this run settles at once, where NeoPi would idle into its
// deadline.
func (c *call) socket(ws *socket, start string, responses, replay []string) (string, error) {
	t := c.t
	settledCh := make(chan settlement, 1)
	settled := false
	settle := func(result string, err error) {
		if settled {
			return
		}
		settled = true
		settledCh <- settlement{result, err}
	}
	abort := func() {
		ws.close()
		settle(resultClosed, &Error{Message: "GitLab Duo Workflow request aborted"})
	}
	if c.ctx.Err() != nil {
		abort()
		return c.wait(settledCh, abort)
	}
	active := c.sess.active
	handle := func(result, data string, remaining []string) bool {
		switch result {
		case resultPause:
			if active != nil {
				active.paused = true
				queue := append([]string{data}, remaining...)
				active.pauseQueue = append(queue, active.pauseQueue...)
			}
			t.pause()
			settle(resultPause, nil)
			return false
		case resultAction:
			// The tool's answer comes back on this same socket.
			settle(resultAction, nil)
			return false
		case resultContinue:
			return true
		}
		ws.close()
		settle(result, nil)
		return false
	}
	ws.onError = func(err error) {
		settle(resultClosed, &Error{Message: "GitLab Duo Workflow WebSocket error: " + describeSocketError(err)})
	}
	ws.onClose = func(code int, reason string) {
		if code >= 4000 && code < 5000 {
			status := code - 4000
			suffix := ""
			if reason != "" {
				suffix = ": " + reason
			}
			if status >= 100 && status <= 599 {
				settle(resultClosed, &Error{Message: fmt.Sprintf("GitLab Duo Workflow closed the session with HTTP %d (WebSocket %d)%s", status, code, suffix), Status: status})
			} else {
				settle(resultClosed, &Error{Message: fmt.Sprintf("GitLab Duo Workflow closed the session (WebSocket %d)%s", code, suffix)})
			}
			return
		}
		if t.lastApprovalStatus != "" {
			settle(resultApproval, nil)
		} else {
			settle(resultClosed, nil)
		}
	}
	ws.onMessage = func(data string) {
		if active != nil && active.paused {
			active.pauseQueue = append(active.pauseQueue, data)
			return
		}
		result, err := t.handleMessage(data)
		if err != nil {
			settle(resultClosed, err)
			return
		}
		handle(result, data, nil)
	}
	switch {
	case len(replay) > 0:
		ws.onOpen = nil
		c.replay(active, replay, handle, settle, &settled)
	case len(responses) > 0:
		ws.onOpen = nil
		for _, response := range responses {
			if err := ws.send(response); err != nil {
				settle(resultClosed, &Error{Message: "GitLab Duo Workflow WebSocket error: " + describeSocketError(err)})
				break
			}
		}
	default:
		ws.onOpen = func() {
			if err := ws.send(start); err != nil {
				settle(resultClosed, &Error{Message: "GitLab Duo Workflow WebSocket error: " + describeSocketError(err)})
			}
		}
	}
	if ws.closed && !settled {
		settle(resultClosed, nil)
	}
	return c.wait(settledCh, abort)
}

type settlement struct {
	result string
	err    error
}

// wait yields the loop until the socket run settles; the request ending
// first aborts it, as NeoPi's abort signal does.
func (c *call) wait(settledCh chan settlement, abort func()) (string, error) {
	c.sess.mu.Unlock()
	var out settlement
	select {
	case out = <-settledCh:
	case <-c.ctx.Done():
		c.sess.mu.Lock()
		abort()
		c.sess.mu.Unlock()
		out = <-settledCh
	}
	c.sess.mu.Lock()
	return out.result, out.err
}

// replay feeds a paused turn's buffered frames through the handler, with
// frames that arrive meanwhile queued behind them.
func (c *call) replay(active *activeSession, replay []string, handle func(string, string, []string) bool, settle func(string, error), settled *bool) {
	if active != nil {
		active.paused = true
	}
	pending := append([]string(nil), replay...)
	for !*settled {
		if len(pending) == 0 {
			if active != nil && len(active.pauseQueue) > 0 {
				pending, active.pauseQueue = append(pending, active.pauseQueue...), nil
				continue
			}
			break
		}
		data := pending[0]
		pending = pending[1:]
		result, err := c.t.handleMessage(data)
		if err != nil {
			settle(resultClosed, err)
			return
		}
		if !handle(result, data, pending) {
			// NeoPi clears the pause flag on any settling frame, so a frame
			// arriving later is handled live rather than buffered.
			if active != nil {
				active.paused = false
			}
			return
		}
		if active != nil && len(active.pauseQueue) > 0 {
			pending, active.pauseQueue = append(pending, active.pauseQueue...), nil
		}
	}
	if !*settled && active != nil {
		active.paused = false
	}
}

func describeSocketError(err error) string {
	if err == nil {
		return "type=error"
	}
	return "type=error, message=" + err.Error()
}

// goalOverflowMessage is buildGitLabDuoWorkflowGoalOverflowMessage, phrased
// as a context overflow so the client compacts.
func goalOverflowMessage(goalBytes int) string {
	return fmt.Sprintf("prompt is too long: %d bytes exceeds the GitLab Duo Agent goal byte budget (soft %d, hard %d)", goalBytes, goalSoftOverflowBytes, goalHardOverflowBytes)
}

type resolvedAction struct {
	requestID string
	result    cursorwire.Message
}

// resolveActionBatch is resolveGitLabDuoWorkflowActionBatch: each pending
// action's tool result, or false while one is missing.
func resolveActionBatch(messages []cursorwire.Message, actions []*action) ([]resolvedAction, bool) {
	resolved := make([]resolvedAction, 0, len(actions))
	for _, pending := range actions {
		found := false
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "toolResult" && messages[i].ToolCallID == pending.requestID {
				resolved = append(resolved, resolvedAction{requestID: pending.requestID, result: messages[i]})
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return resolved, true
}

// steerAfterBatch is hasGitLabDuoWorkflowSteerAfterBatch: a user or
// developer message after the batch's last tool result.
func steerAfterBatch(messages []cursorwire.Message, batch []resolvedAction) bool {
	ids := map[string]bool{}
	for _, entry := range batch {
		ids[entry.requestID] = true
	}
	last := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "toolResult" && ids[messages[i].ToolCallID] {
			last = i
			break
		}
	}
	if last < 0 {
		return false
	}
	for _, message := range messages[last+1:] {
		if message.Role == "user" || message.Role == "developer" {
			return true
		}
	}
	return false
}

// actionResponse is buildGitLabDuoWorkflowActionResponse.
func actionResponse(requestID string, result cursorwire.Message) json.RawMessage {
	return jsjson.Object(jsjson.Field{Name: "actionResponse", Value: jsjson.Object(
		jsjson.Field{Name: "requestID", Value: jsjson.Marshal(requestID)},
		jsjson.Field{Name: "plainTextResponse", Value: ToolResultAnswer(result)},
	)})
}

// firstNonEmpty is a `??` chain of nonEmptyString values.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if jsTrim(value) != "" {
			return value
		}
	}
	return ""
}

// firstDefined is a `??` chain over values that are absent when empty.
func firstDefined(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// signature keys the checkpoint dedupe by turn position, kind and content.
type signature struct {
	turn int
	kind string
	hash uint64
}

var contentSeed = maphash.MakeSeed()

// turn is GitLabDuoWorkflowStreamState. Its events pass through the
// leaked-thinking projector on their way to the sink.
type turn struct {
	projector          *neopidialect.Projector
	done               bool
	output             *Output
	activeText         int
	activeThinking     int
	activeKey          string
	byKey              map[string]string
	signatures         map[signature]bool
	pauseRequested     bool
	stepLimitRequested bool
	retryableRequested bool
	stalledRequested   bool
	lastLength         int
	hasLastLength      bool
	session            *session
	lastApprovalStatus string
	goalOverflow       string
}

func newTurn(sink func(Event)) *turn {
	return &turn{projector: neopidialect.NewProjector(sink), output: &Output{Content: []*cursorwire.OutBlock{}, StopReason: "stop"}, activeText: -1, activeThinking: -1}
}

// push is the event stream's push: nothing after done or error.
func (t *turn) push(event Event) {
	if t.done {
		return
	}
	if event.Type == "done" || event.Type == "error" {
		t.done = true
	}
	t.projector.Push(event, t.output.Content)
}

// handleMessage is handleGitLabDuoWorkflowSocketMessage.
func (t *turn) handleMessage(data string) (string, error) {
	event := parseFrame(data)
	if event == nil {
		return resultContinue, nil
	}
	status, _ := frameStatus(event)
	if snapshot := extractCheckpoint(event); snapshot != nil {
		t.emitCheckpoint(snapshot)
	}
	if t.pauseRequested {
		t.pauseRequested = false
		return resultPause, nil
	}
	if approvalStatus(status) {
		t.lastApprovalStatus = status
		return resultApproval, nil
	}
	if completionStatus(status) {
		t.finish("stop")
		return resultTerminal, nil
	}
	if status == "FAILED" || status == "STOPPED" {
		message, ok := firstRecordString(recordString(event, "error"), recordString(event, "message"))
		if !ok {
			message = status
		}
		if status == "FAILED" && stepLimitMessage(message) {
			t.stepLimitRequested = true
			return resultStepLimit, nil
		}
		if status == "FAILED" && genericProcessingError(message) {
			// Surfaced only once the fresh-workflow retry fails too.
			t.retryableRequested = true
			t.output.ErrorMessage = message
			return resultRetryable, nil
		}
		t.output.StopReason = "error"
		t.output.ErrorMessage = firstDefined(t.goalOverflow, message)
		t.push(Event{Type: "error"})
		return resultTerminal, nil
	}
	next, err := extractAction(event)
	if err != nil {
		return "", err
	}
	if next == nil {
		return resultContinue, nil
	}
	if t.detectStall() {
		t.stalledRequested = true
		return resultStalled, nil
	}
	t.emitAction(next)
	return resultAction, nil
}

// detectStall is detectGitLabDuoWorkflowStall: this tool-call boundary's
// checkpoint is byte-for-byte as long as the previous boundary's.
func (t *turn) detectStall() bool {
	active := t.session.active
	if active == nil || !t.hasLastLength {
		return false
	}
	stalled := active.hasLastBoundary && active.lastBoundaryLength == t.lastLength
	active.lastBoundaryLength, active.hasLastBoundary = t.lastLength, true
	return stalled
}

// emitAction is emitGitLabDuoWorkflowActionToolCall: the action as this
// turn's tool call, then the turn ends and the action waits for its result.
func (t *turn) emitAction(next *action) {
	t.endText()
	t.endThinking()
	block := next.toolCall()
	t.output.Content = append(t.output.Content, block)
	index := len(t.output.Content) - 1
	t.push(Event{Type: "toolcall_start", ContentIndex: index, Block: block})
	t.push(Event{Type: "toolcall_delta", ContentIndex: index, Delta: cursorwire.StringifyJSON(block.Arguments), Block: block})
	t.push(Event{Type: "toolcall_end", ContentIndex: index, Block: block})
	t.finish("toolUse")
	if active := t.session.active; active != nil {
		active.pending = []*action{next}
	}
}

// emitCheckpoint is emitGitLabDuoWorkflowCheckpoint: every checkpoint is a
// full ui_chat_log snapshot, so each agent message emits only what it grew
// by. A boundary after a delta of this same snapshot pauses the turn.
func (t *turn) emitCheckpoint(snapshot *checkpoint) {
	if snapshot.contextUsage != nil {
		usage := &t.output.Usage
		usage.Input = int64(snapshot.contextUsage.used)
		usage.Total = usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
	}
	t.lastLength, t.hasLastLength = snapshot.contentLength, true
	deltaThisCheckpoint := false
	turnIndex := 0
	for _, entry := range snapshot.entries {
		if entry.kind == kindBoundary {
			if deltaThisCheckpoint && t.session.active != nil {
				t.pauseRequested = true
				return
			}
			t.endText()
			t.endThinking()
			turnIndex++
			continue
		}
		if t.byKey == nil {
			t.byKey = map[string]string{}
		}
		if t.signatures == nil {
			t.signatures = map[signature]bool{}
		}
		previous, seen := t.byKey[entry.messageKey]
		hash := maphash.String(contentSeed, entry.content)
		kindSignature := signature{turn: turnIndex, kind: entry.kind, hash: hash}
		contentSignature := signature{turn: turnIndex, kind: "content", hash: hash}
		duplicate := !seen && (t.signatures[kindSignature] || t.signatures[contentSignature])
		rewrote := seen && !strings.HasPrefix(entry.content, previous) && previous != entry.content
		delta := ""
		switch {
		case duplicate, rewrote:
		case seen:
			delta = entry.content[len(previous):]
		default:
			delta = entry.content
		}
		t.byKey[entry.messageKey] = entry.content
		t.signatures[kindSignature] = true
		t.signatures[contentSignature] = true
		if active := t.session.active; active != nil {
			active.byKey, active.signatures = t.byKey, t.signatures
		}
		if delta == "" {
			continue
		}
		if t.activeKey != "" && t.activeKey != entry.messageKey && !seen {
			t.endText()
			t.endThinking()
		}
		if entry.kind == kindThinking {
			t.emitThinking(delta)
		} else {
			t.emitText(delta)
		}
		t.activeKey = entry.messageKey
		deltaThisCheckpoint = true
	}
}

func (t *turn) emitText(text string) {
	t.endThinking()
	if t.activeText < 0 {
		t.output.Content = append(t.output.Content, &cursorwire.OutBlock{Type: "text"})
		t.activeText = len(t.output.Content) - 1
		t.push(Event{Type: "text_start", ContentIndex: t.activeText})
	}
	t.output.Content[t.activeText].Text += text
	t.push(Event{Type: "text_delta", ContentIndex: t.activeText, Delta: text})
}

func (t *turn) emitThinking(thinking string) {
	t.endText()
	if t.activeThinking < 0 {
		t.output.Content = append(t.output.Content, &cursorwire.OutBlock{Type: "thinking"})
		t.activeThinking = len(t.output.Content) - 1
		t.push(Event{Type: "thinking_start", ContentIndex: t.activeThinking})
	}
	t.output.Content[t.activeThinking].Text += thinking
	t.push(Event{Type: "thinking_delta", ContentIndex: t.activeThinking, Delta: thinking})
}

func (t *turn) endText() {
	if t.activeText < 0 {
		return
	}
	t.push(Event{Type: "text_end", ContentIndex: t.activeText})
	t.activeText = -1
}

func (t *turn) endThinking() {
	if t.activeThinking < 0 {
		return
	}
	t.push(Event{Type: "thinking_end", ContentIndex: t.activeThinking})
	t.activeThinking = -1
}

// finish is finishGitLabDuoWorkflowStream.
func (t *turn) finish(reason string) {
	t.endText()
	t.endThinking()
	t.output.StopReason = reason
	t.push(Event{Type: "done"})
}

// pause is pauseGitLabDuoWorkflowStream: the turn ends as a pause_turn stop.
func (t *turn) pause() {
	t.endText()
	t.endThinking()
	t.output.StopReason = "stop"
	t.output.StopDetails = "pause_turn"
	t.push(Event{Type: "done"})
}
