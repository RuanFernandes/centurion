package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/security"
)

type AppServer struct {
	client *Client

	lifecycleMu     sync.Mutex
	mu              sync.RWMutex
	auth            model.AuthState
	models          []model.ModelInfo
	mcpServers      []model.MCPServer
	capabilities    model.AppCapabilities
	sessions        *SessionManager
	onNotification  func(model.CodexNotification)
	onServerRequest func(ServerRequest)
	sequence        int64
	rateLimitMu     sync.Mutex
}

type AgentTurnResult struct {
	ThreadID string
	TurnID   string
	Status   string
	Output   string
	Error    string
}

func (a *AppServer) Diagnostics() []string {
	return a.client.Diagnostics()
}

func NewAppServer(command string, onNotification func(model.CodexNotification), onServerRequest func(ServerRequest)) *AppServer {
	return newAppServerWithClient(NewClient(command), onNotification, onServerRequest)
}

func newAppServerWithClient(client *Client, onNotification func(model.CodexNotification), onServerRequest func(ServerRequest)) *AppServer {
	appServer := &AppServer{
		client:          client,
		onNotification:  onNotification,
		onServerRequest: onServerRequest,
		sessions:        NewSessionManager(16),
		auth:            model.AuthState{Status: model.AuthStatusOffline, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)},
		capabilities:    model.AppCapabilities{ExperimentalAPI: true},
	}
	appServer.client.SetServerRequestHandler(appServer.handleServerRequest)
	return appServer
}

func (a *AppServer) Start(ctx context.Context) error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.client.IsConnected() {
		return nil
	}
	if err := a.client.Start(ctx); err != nil {
		a.setAuth(model.AuthState{Status: model.AuthStatusOffline, Error: err.Error(), UpdatedAt: now()})
		return err
	}
	notifications, cancel := a.client.Subscribe()
	go func() {
		defer cancel()
		for notification := range notifications {
			a.handleNotification(notification)
		}
	}()

	initializeParams := map[string]any{
		"clientInfo": map[string]any{
			"name":    "centurion",
			"title":   "Centurion",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	}
	var initializeResult map[string]any
	if err := a.client.Call(ctx, "initialize", initializeParams, &initializeResult); err != nil {
		_ = a.client.Stop()
		a.setAuth(model.AuthState{Status: model.AuthStatusError, Error: err.Error(), UpdatedAt: now()})
		return fmt.Errorf("initialize app-server: %w", err)
	}
	if err := a.client.Notify(ctx, "initialized", map[string]any{}); err != nil {
		_ = a.client.Stop()
		return fmt.Errorf("acknowledge app-server initialization: %w", err)
	}
	a.mu.Lock()
	a.capabilities.Methods = extractStringSlice(initializeResult["methods"])
	a.mu.Unlock()

	refreshCtx, cancelRefresh := context.WithTimeout(ctx, 15*time.Second)
	defer cancelRefresh()
	var waitGroup sync.WaitGroup
	var accountErr, mcpErr error
	waitGroup.Add(3)
	go func() {
		defer waitGroup.Done()
		accountErr = a.RefreshAccount(refreshCtx)
	}()
	go func() {
		defer waitGroup.Done()
		_, _ = a.ListModels(refreshCtx)
	}()
	go func() {
		defer waitGroup.Done()
		_, mcpErr = a.ListMCPServers(refreshCtx)
	}()
	waitGroup.Wait()
	if accountErr != nil {
		a.setAuth(model.AuthState{Status: model.AuthStatusError, Error: fmt.Sprintf("confirm ChatGPT login: %v", accountErr), UpdatedAt: now()})
		a.client.addDiagnostic(fmt.Sprintf("account/read: %v", accountErr))
	}
	if mcpErr != nil {
		a.client.addDiagnostic(fmt.Sprintf("mcpServerStatus/list: %v", mcpErr))
	}
	return nil
}

func (a *AppServer) Stop() error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	return a.client.Stop()
}

func (a *AppServer) IsConnected() bool { return a.client.IsConnected() }

func (a *AppServer) RuntimeStatus() model.RuntimeStatus {
	state := a.AuthState()
	stats := a.sessions.Stats()
	return model.RuntimeStatus{
		Connected:      a.client.IsConnected(),
		CodexCommand:   a.client.Command(),
		AppServerPID:   a.client.PID(),
		ActiveSessions: stats.Active,
		MaxSessions:    stats.Max,
		AuthStatus:     state.Status,
		UpdatedAt:      now(),
	}
}

func (a *AppServer) AuthState() model.AuthState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.auth
}

func (a *AppServer) Models() []model.ModelInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]model.ModelInfo(nil), a.models...)
}

func (a *AppServer) MCPServers() []model.MCPServer {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]model.MCPServer(nil), a.mcpServers...)
}

func (a *AppServer) CanRun() error {
	a.mu.RLock()
	auth := a.auth
	a.mu.RUnlock()
	if !a.client.IsConnected() {
		return ErrNotConnected
	}
	if auth.RateLimit != nil && auth.RateLimit.IsReached() {
		return errors.New("codex rate limit reached; automatic runs are paused")
	}
	if auth.Status == model.AuthStatusLoggedOut || (auth.Status == model.AuthStatusError && auth.RequiresOpenAIAuth) {
		return errors.New("Codex is not authenticated; sign in through the Codex CLI. Centurion does not manage account login")
	}
	return nil
}

func (a *AppServer) RefreshAccount(ctx context.Context) error {
	var result struct {
		Account *struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
		RequiresOpenAIAuth bool `json:"requiresOpenaiAuth"`
	}
	if err := a.client.Call(ctx, "account/read", map[string]any{"refreshToken": false}, &result); err != nil {
		return err
	}
	state := model.AuthState{Status: model.AuthStatusLoggedOut, RequiresOpenAIAuth: result.RequiresOpenAIAuth, UpdatedAt: now()}
	if result.Account != nil {
		state.Status = model.AuthStatusLoggedIn
		state.AccountType = result.Account.Type
		state.Email = result.Account.Email
		state.Plan = result.Account.PlanType
	}
	a.setAuth(state)
	return a.RefreshRateLimits(ctx)
}

func (a *AppServer) BeginChatGPTLogin(ctx context.Context) (model.LoginStart, error) {
	var result model.LoginStart
	if err := a.client.Call(ctx, "account/login/start", map[string]any{
		"type":                      "chatgpt",
		"useHostedLoginSuccessPage": true,
		"appBrand":                  "chatgpt",
	}, &result); err != nil {
		return model.LoginStart{}, err
	}
	if result.Type == "" {
		result.Type = "chatgpt"
	}
	return result, nil
}

func (a *AppServer) Logout(ctx context.Context) error {
	var result map[string]any
	if err := a.client.Call(ctx, "account/logout", nil, &result); err != nil {
		return err
	}
	a.setAuth(model.AuthState{Status: model.AuthStatusLoggedOut, UpdatedAt: now()})
	return nil
}

func (a *AppServer) ListModels(ctx context.Context) ([]model.ModelInfo, error) {
	var result struct {
		Data []struct {
			ID                        string            `json:"id"`
			Model                     string            `json:"model"`
			DisplayName               string            `json:"displayName"`
			Description               string            `json:"description"`
			DefaultReasoningEffort    string            `json:"defaultReasoningEffort"`
			SupportedReasoningEfforts []json.RawMessage `json:"supportedReasoningEfforts"`
			InputModalities           []string          `json:"inputModalities"`
			SupportsPersonality       bool              `json:"supportsPersonality"`
			IsDefault                 bool              `json:"isDefault"`
		} `json:"data"`
	}
	if err := a.client.Call(ctx, "model/list", map[string]any{"limit": 100, "includeHidden": false}, &result); err != nil {
		return nil, err
	}
	models := make([]model.ModelInfo, 0, len(result.Data))
	for _, entry := range result.Data {
		id := entry.ID
		if id == "" {
			id = entry.Model
		}
		modalities := entry.InputModalities
		if len(modalities) == 0 {
			modalities = []string{"text", "image"}
		}
		models = append(models, model.ModelInfo{
			ID:                        id,
			DisplayName:               firstNonEmpty(entry.DisplayName, id),
			Description:               entry.Description,
			DefaultReasoningEffort:    entry.DefaultReasoningEffort,
			SupportedReasoningEfforts: parseReasoningEfforts(entry.SupportedReasoningEfforts),
			InputModalities:           modalities,
			SupportsPersonality:       entry.SupportsPersonality,
			IsDefault:                 entry.IsDefault,
		})
	}
	a.mu.Lock()
	a.models = models
	a.mu.Unlock()
	return models, nil
}

func (a *AppServer) ListMCPServers(ctx context.Context) ([]model.MCPServer, error) {
	servers := make([]model.MCPServer, 0)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for page := 0; page < 100; page++ {
		params := map[string]any{"limit": 100, "detail": "full"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Data       []json.RawMessage `json:"data"`
			Servers    []json.RawMessage `json:"servers"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := a.client.Call(ctx, "mcpServerStatus/list", params, &result); err != nil {
			return nil, err
		}
		entries := result.Data
		if len(entries) == 0 {
			entries = result.Servers
		}
		for _, raw := range entries {
			if server, ok := parseMCPServerStatusEntry(raw); ok {
				servers = append(servers, server)
			}
		}
		nextCursor := strings.TrimSpace(result.NextCursor)
		if nextCursor == "" {
			break
		}
		if _, exists := seenCursors[nextCursor]; exists {
			break
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}
	a.mu.Lock()
	a.mcpServers = servers
	a.mu.Unlock()
	return servers, nil
}

type mcpServerStatusEntry struct {
	Name              string            `json:"name"`
	ID                string            `json:"id"`
	Status            json.RawMessage   `json:"status"`
	AuthStatus        json.RawMessage   `json:"authStatus"`
	Error             json.RawMessage   `json:"error"`
	Tools             json.RawMessage   `json:"tools"`
	Resources         []json.RawMessage `json:"resources"`
	ResourceTemplates []json.RawMessage `json:"resourceTemplates"`
}

func parseMCPServerStatusEntry(raw json.RawMessage) (model.MCPServer, bool) {
	var entry mcpServerStatusEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return model.MCPServer{}, false
	}
	id := firstNonEmpty(entry.Name, entry.ID)
	if id == "" {
		return model.MCPServer{}, false
	}

	tools := parseMCPTools(entry.Tools)
	resources := parseMCPResources(entry.Resources)
	authStatus := jsonStringValue(entry.AuthStatus)
	errorMessage := redactDiagnostic(jsonStringValue(entry.Error))
	status := jsonStringValue(entry.Status)
	if status == "" {
		switch {
		case errorMessage != "":
			status = "error"
		case len(tools) > 0 || len(resources) > 0:
			status = "ready"
		case strings.EqualFold(authStatus, "unsupported"):
			status = "unsupported"
		default:
			status = "configured"
		}
	}

	return model.MCPServer{
		ID:         id,
		Name:       firstNonEmpty(entry.Name, id),
		Status:     status,
		AuthStatus: authStatus,
		Tools:      tools,
		Resources:  resources,
		Error:      errorMessage,
	}, true
}

func parseMCPTools(raw json.RawMessage) []model.MCPTool {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	type toolEntry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	var list []toolEntry
	if json.Unmarshal(raw, &list) == nil {
		result := make([]model.MCPTool, 0, len(list))
		for _, tool := range list {
			if tool.Name != "" {
				result = append(result, model.MCPTool{Name: tool.Name, Description: tool.Description})
			}
		}
		return result
	}
	var keyed map[string]toolEntry
	if json.Unmarshal(raw, &keyed) != nil {
		return nil
	}
	keys := make([]string, 0, len(keyed))
	for key := range keyed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]model.MCPTool, 0, len(keys))
	for _, key := range keys {
		tool := keyed[key]
		result = append(result, model.MCPTool{Name: firstNonEmpty(tool.Name, key), Description: tool.Description})
	}
	return result
}

func parseMCPResources(raw []json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	resources := make([]string, 0, len(raw))
	for _, item := range raw {
		var value string
		if json.Unmarshal(item, &value) == nil && strings.TrimSpace(value) != "" {
			resources = append(resources, strings.TrimSpace(value))
			continue
		}
		var resource struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(item, &resource) == nil && resource.URI != "" {
			resources = append(resources, resource.URI)
			continue
		}
		if value := jsonStringValue(item); value != "" {
			resources = append(resources, value)
		}
	}
	return resources
}

func jsonStringValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	for _, key := range []string{"message", "type", "status", "name"} {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (a *AppServer) LoginMCPServer(ctx context.Context, serverID string) (string, error) {
	var result struct {
		AuthURL string `json:"authUrl"`
		URL     string `json:"url"`
	}
	if err := a.client.Call(ctx, "mcpServer/oauth/login", map[string]any{"name": serverID}, &result); err != nil {
		return "", err
	}
	return firstNonEmpty(result.AuthURL, result.URL), nil
}

func (a *AppServer) ReloadMCPServers(ctx context.Context) error {
	var result map[string]any
	if err := a.client.Call(ctx, "config/mcpServer/reload", nil, &result); err != nil {
		return err
	}
	_, err := a.ListMCPServers(ctx)
	return err
}

func (a *AppServer) RunAgentTurn(ctx context.Context, agent model.AgentProfile, prompt string, existingThreadID string, onTurnStarted func(threadID, turnID string), onEvent func(Notification)) (AgentTurnResult, error) {
	return a.runAgentTurn(ctx, agent, prompt, existingThreadID, nil, onTurnStarted, onEvent)
}

func (a *AppServer) AgentForThread(threadID string) (string, bool) {
	if a == nil {
		return "", false
	}
	return a.sessions.AgentForThread(threadID)
}

// RunAgentTurnWithOutputSchema uses the stable turn-level structured output
// contract when the installed App Server supports it. The regular executor
// keeps using RunAgentTurn because its node outputs are intentionally free-form.
func (a *AppServer) RunAgentTurnWithOutputSchema(ctx context.Context, agent model.AgentProfile, prompt string, existingThreadID string, outputSchema map[string]any, onTurnStarted func(threadID, turnID string), onEvent func(Notification)) (AgentTurnResult, error) {
	return a.runAgentTurn(ctx, agent, prompt, existingThreadID, outputSchema, onTurnStarted, onEvent)
}

func (a *AppServer) runAgentTurn(ctx context.Context, agent model.AgentProfile, prompt string, existingThreadID string, outputSchema map[string]any, onTurnStarted func(threadID, turnID string), onEvent func(Notification)) (AgentTurnResult, error) {
	if err := a.CanRun(); err != nil {
		return AgentTurnResult{}, err
	}
	existingThreadID = strings.TrimSpace(existingThreadID)
	session, err := a.sessions.Open(agent.ID, existingThreadID)
	if err != nil {
		return AgentTurnResult{}, err
	}
	defer a.sessions.Close(session.ID)
	isNewThread := existingThreadID == ""
	threadID := existingThreadID
	sandbox, sandboxPolicy := agentSandbox(agent)
	if threadID == "" {
		params := map[string]any{
			"approvalPolicy": security.ApprovalPolicy(agent.ApprovalProfile),
			"serviceName":    "centurion",
			"sandbox":        sandbox,
		}
		if agent.ModelID != "" {
			params["model"] = agent.ModelID
		}
		if len(agent.WorkspaceRoots) > 0 {
			params["cwd"] = agent.WorkspaceRoots[0]
		}
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := a.client.Call(ctx, "thread/start", params, &result); err != nil {
			return AgentTurnResult{}, err
		}
		threadID = result.Thread.ID
		if threadID == "" {
			return AgentTurnResult{}, errors.New("app-server returned an empty thread id")
		}
	} else {
		var result map[string]any
		if err := a.client.Call(ctx, "thread/resume", map[string]any{"threadId": threadID}, &result); err != nil {
			return AgentTurnResult{}, err
		}
	}
	a.sessions.Touch(session.ID, threadID)

	notifications, cancel := a.client.Subscribe()
	defer cancel()
	params := map[string]any{
		"threadId": threadID,
		"input":    []map[string]any{{"type": "text", "text": prompt}},
	}
	addInitialTurnOverrides(params, agent, isNewThread)
	if len(agent.WorkspaceRoots) > 0 {
		params["cwd"] = agent.WorkspaceRoots[0]
	}
	params["sandboxPolicy"] = sandboxPolicy
	if outputSchema != nil {
		params["outputSchema"] = outputSchema
	}
	var turnResponse struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := a.client.Call(ctx, "turn/start", params, &turnResponse); err != nil {
		return AgentTurnResult{ThreadID: threadID}, err
	}
	turnID := turnResponse.Turn.ID
	if turnID == "" {
		return AgentTurnResult{ThreadID: threadID}, errors.New("app-server returned an empty turn id")
	}
	if onTurnStarted != nil {
		onTurnStarted(threadID, turnID)
	}

	var output strings.Builder
	for {
		select {
		case <-ctx.Done():
			interruptCtx, cancelInterrupt := context.WithTimeout(context.Background(), 3*time.Second)
			_ = a.Interrupt(interruptCtx, threadID, turnID)
			cancelInterrupt()
			return AgentTurnResult{ThreadID: threadID, TurnID: turnID, Status: "interrupted", Output: output.String()}, ctx.Err()
		case notification, ok := <-notifications:
			if !ok {
				return AgentTurnResult{ThreadID: threadID, TurnID: turnID, Status: "interrupted", Output: output.String()}, ErrClosed
			}
			if onEvent != nil {
				onEvent(notification)
			}
			if delta, ok := agentMessageDelta(notification, turnID); ok {
				output.WriteString(delta)
			}
			if text, ok := completedAgentMessage(notification, turnID); ok && output.Len() == 0 {
				output.WriteString(text)
			}
			if status, errMessage, ok := completedTurn(notification, turnID); ok {
				result := AgentTurnResult{ThreadID: threadID, TurnID: turnID, Status: status, Output: output.String(), Error: errMessage}
				if status != "completed" {
					if errMessage == "" {
						errMessage = "Codex turn finished with status " + status
					}
					return result, errors.New(errMessage)
				}
				return result, nil
			}
		}
	}
}

// agentSandbox maps Centurion's capability policy to the App Server sandbox.
// ToolAllowlist entries are Centurion policy IDs, not literal App Server tool
// names. File-write capability is nevertheless enforceable at the sandbox
// boundary, so profiles without it cannot modify their workspace.
func agentSandbox(agent model.AgentProfile) (string, map[string]any) {
	if agentCanWriteWorkspace(agent.ToolAllowlist) && len(agent.WorkspaceRoots) > 0 {
		return "workspaceWrite", security.SandboxPolicy(agent.WorkspaceRoots, false)
	}
	return "readOnly", security.ReadOnlySandboxPolicy(agent.WorkspaceRoots)
}

func agentCanWriteWorkspace(permissions []string) bool {
	for _, permission := range permissions {
		if strings.EqualFold(strings.TrimSpace(permission), "files.write") {
			return true
		}
	}
	return false
}

// addInitialTurnOverrides sets model configuration once when a thread is
// created. App Server persists turn-level model and effort settings on that
// thread, so repeating them after thread/resume adds noise and can override a
// configuration the user intentionally established in the conversation.
func addInitialTurnOverrides(params map[string]any, agent model.AgentProfile, isNewThread bool) {
	if !isNewThread {
		return
	}
	if agent.ModelID != "" {
		params["model"] = agent.ModelID
	}
	if agent.ReasoningEffort != "" {
		params["effort"] = agent.ReasoningEffort
	}
}

func (a *AppServer) Interrupt(ctx context.Context, threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return nil
	}
	var result map[string]any
	return a.client.Call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, &result)
}

func (a *AppServer) Steer(ctx context.Context, threadID, turnID, message string) error {
	var result map[string]any
	return a.client.Call(ctx, "turn/steer", map[string]any{
		"threadId":       threadID,
		"expectedTurnId": turnID,
		"input":          []map[string]any{{"type": "text", "text": message}},
	}, &result)
}

func (a *AppServer) RespondServerRequest(ctx context.Context, id []byte, result any) error {
	return a.client.Respond(ctx, id, result, nil)
}

func (a *AppServer) RespondServerRequestError(ctx context.Context, id []byte, rpcErr *RPCError) error {
	return a.client.Respond(ctx, id, nil, rpcErr)
}

func (a *AppServer) handleServerRequest(request ServerRequest) {
	if a.onServerRequest != nil {
		a.onServerRequest(request)
		return
	}
	_ = a.client.Respond(context.Background(), request.ID, map[string]any{"decision": "decline"}, nil)
}

func (a *AppServer) handleNotification(notification Notification) {
	params := make(map[string]any)
	if len(notification.Params) > 0 {
		_ = json.Unmarshal(notification.Params, &params)
	}
	switch notification.Method {
	case "account/updated":
		state := a.AuthState()
		if mode, ok := params["authMode"].(string); ok {
			state.AccountType = mode
			if mode == "chatgpt" || mode == "apikey" {
				state.Status = model.AuthStatusLoggedIn
			} else if mode == "" {
				state.Status = model.AuthStatusLoggedOut
			}
		}
		if plan, ok := params["planType"].(string); ok {
			state.Plan = plan
		}
		state.UpdatedAt = now()
		a.setAuth(state)
		if state.Status == model.AuthStatusLoggedIn {
			a.refreshRateLimitsAsync()
		}
	case "account/login/completed":
		if success, ok := params["success"].(bool); ok && success {
			state := a.AuthState()
			state.Status = model.AuthStatusLoggedIn
			state.Error = ""
			state.UpdatedAt = now()
			a.setAuth(state)
			a.refreshRateLimitsAsync()
		} else if ok && !success {
			state := a.AuthState()
			state.Status = model.AuthStatusError
			if message, ok := params["error"].(string); ok {
				state.Error = message
			}
			state.UpdatedAt = now()
			a.setAuth(state)
		}
	case "account/rateLimits/updated":
		a.applyRateLimit(params)
	case "mcpServer/startupStatus/updated", "mcpServer/oauthLogin/completed":
		if a.onNotification != nil {
			a.onNotification(a.notificationModel(notification))
		}
	}
	if a.onNotification != nil && notification.Method != "account/rateLimits/updated" {
		a.onNotification(a.notificationModel(notification))
	}
}

type rateLimitWindowPayload struct {
	UsedPercent        int   `json:"usedPercent"`
	WindowDurationMins int   `json:"windowDurationMins"`
	ResetsAt           int64 `json:"resetsAt"`
}

type rateLimitBucketPayload struct {
	LimitID              string                  `json:"limitId"`
	LimitName            string                  `json:"limitName"`
	Primary              *rateLimitWindowPayload `json:"primary"`
	Secondary            *rateLimitWindowPayload `json:"secondary"`
	RateLimitReachedType string                  `json:"rateLimitReachedType"`
}

type rateLimitResponsePayload struct {
	RateLimits          rateLimitBucketPayload            `json:"rateLimits"`
	RateLimitsByLimitID map[string]rateLimitBucketPayload `json:"rateLimitsByLimitId"`
}

func (a *AppServer) RefreshRateLimits(ctx context.Context) error {
	a.rateLimitMu.Lock()
	defer a.rateLimitMu.Unlock()
	return a.refreshRateLimit(ctx)
}

func (a *AppServer) refreshRateLimitsAsync() {
	if !a.client.IsConnected() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = a.RefreshRateLimits(ctx)
	}()
}

func (a *AppServer) refreshRateLimit(ctx context.Context) error {
	var result rateLimitResponsePayload
	if err := a.client.Call(ctx, "account/rateLimits/read", nil, &result); err != nil {
		return err
	}
	snapshot := buildRateLimitSnapshot(result)
	state := a.AuthState()
	state.RateLimit = snapshot
	state.UpdatedAt = now()
	a.setAuth(state)
	return nil
}

func buildRateLimitSnapshot(result rateLimitResponsePayload) *model.RateLimitSnapshot {
	selected := result.RateLimits
	buckets := make([]model.RateLimitBucket, 0, len(result.RateLimitsByLimitID)+1)
	seen := make(map[string]struct{}, len(result.RateLimitsByLimitID)+1)

	if rateLimitBucketHasData(selected) {
		buckets = append(buckets, rateLimitBucketModel(selected))
		if selected.LimitID != "" {
			seen[selected.LimitID] = struct{}{}
		}
	}

	ids := make([]string, 0, len(result.RateLimitsByLimitID))
	for id := range result.RateLimitsByLimitID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		bucket := result.RateLimitsByLimitID[id]
		if bucket.LimitID == "" {
			bucket.LimitID = id
		}
		if _, exists := seen[bucket.LimitID]; exists {
			continue
		}
		buckets = append(buckets, rateLimitBucketModel(bucket))
		seen[bucket.LimitID] = struct{}{}
	}

	if !rateLimitBucketHasData(selected) && len(ids) > 0 {
		selected = result.RateLimitsByLimitID[ids[0]]
		if selected.LimitID == "" {
			selected.LimitID = ids[0]
		}
	}

	snapshot := &model.RateLimitSnapshot{
		LimitID:              selected.LimitID,
		LimitName:            selected.LimitName,
		RateLimitReachedType: selected.RateLimitReachedType,
		Secondary:            rateLimitWindowModel(selected.Secondary),
		Buckets:              buckets,
	}
	if selected.Primary != nil {
		snapshot.UsedPercent = selected.Primary.UsedPercent
		snapshot.WindowDurationMins = selected.Primary.WindowDurationMins
		snapshot.ResetsAt = selected.Primary.ResetsAt
	}
	return snapshot
}

func rateLimitBucketHasData(bucket rateLimitBucketPayload) bool {
	return bucket.LimitID != "" || bucket.LimitName != "" || bucket.Primary != nil || bucket.Secondary != nil || bucket.RateLimitReachedType != ""
}

func rateLimitBucketModel(bucket rateLimitBucketPayload) model.RateLimitBucket {
	return model.RateLimitBucket{
		LimitID:              bucket.LimitID,
		LimitName:            bucket.LimitName,
		Primary:              rateLimitWindowModel(bucket.Primary),
		Secondary:            rateLimitWindowModel(bucket.Secondary),
		RateLimitReachedType: bucket.RateLimitReachedType,
	}
}

func rateLimitWindowModel(window *rateLimitWindowPayload) *model.RateLimitWindow {
	if window == nil {
		return nil
	}
	return &model.RateLimitWindow{
		UsedPercent:        window.UsedPercent,
		WindowDurationMins: window.WindowDurationMins,
		ResetsAt:           window.ResetsAt,
	}
}

func (a *AppServer) applyRateLimit(params map[string]any) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return
	}
	var result rateLimitResponsePayload
	if json.Unmarshal(encoded, &result) != nil {
		return
	}
	snapshot := buildRateLimitSnapshot(result)
	state := a.AuthState()
	state.RateLimit = snapshot
	state.UpdatedAt = now()
	a.setAuth(state)
}

func (a *AppServer) setAuth(state model.AuthState) {
	if state.UpdatedAt == "" {
		state.UpdatedAt = now()
	}
	a.mu.Lock()
	a.auth = state
	a.mu.Unlock()
	if a.onNotification != nil {
		params := map[string]any{"auth": state}
		raw, _ := json.Marshal(params)
		a.onNotification(model.CodexNotification{SchemaVersion: 1, Timestamp: now(), Sequence: a.nextSequence(), Source: "codex", Method: "account/state", Params: mustObject(raw)})
	}
}

func (a *AppServer) notificationModel(notification Notification) model.CodexNotification {
	params := make(map[string]any)
	if len(notification.Params) > 0 {
		_ = json.Unmarshal(notification.Params, &params)
	}
	return model.CodexNotification{SchemaVersion: 1, Timestamp: now(), Sequence: a.nextSequence(), Source: "codex", Method: notification.Method, Params: params}
}

func (a *AppServer) nextSequence() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sequence++
	return a.sequence
}

func parseReasoningEfforts(raw []json.RawMessage) []model.ReasoningEffort {
	result := make([]model.ReasoningEffort, 0, len(raw))
	for _, item := range raw {
		var object struct {
			ReasoningEffort string `json:"reasoningEffort"`
			Description     string `json:"description"`
		}
		if json.Unmarshal(item, &object) == nil && object.ReasoningEffort != "" {
			result = append(result, model.ReasoningEffort{ReasoningEffort: object.ReasoningEffort, Description: object.Description})
			continue
		}
		var value string
		if json.Unmarshal(item, &value) == nil && value != "" {
			result = append(result, model.ReasoningEffort{ReasoningEffort: value})
		}
	}
	return result
}

func agentMessageDelta(notification Notification, turnID string) (string, bool) {
	if notification.Method != "item/agentMessage/delta" {
		return "", false
	}
	var payload struct {
		TurnID string `json:"turnId"`
		Delta  string `json:"delta"`
	}
	if json.Unmarshal(notification.Params, &payload) != nil || (payload.TurnID != "" && payload.TurnID != turnID) {
		return "", false
	}
	return payload.Delta, payload.Delta != ""
}

func completedAgentMessage(notification Notification, turnID string) (string, bool) {
	if notification.Method != "item/completed" {
		return "", false
	}
	var payload struct {
		TurnID string `json:"turnId"`
		Item   struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(notification.Params, &payload) != nil || (payload.TurnID != "" && payload.TurnID != turnID) || payload.Item.Type != "agentMessage" {
		return "", false
	}
	return payload.Item.Text, payload.Item.Text != ""
}

func completedTurn(notification Notification, turnID string) (string, string, bool) {
	if notification.Method != "turn/completed" {
		return "", "", false
	}
	var payload struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(notification.Params, &payload) != nil || payload.Turn.ID != turnID {
		return "", "", false
	}
	message := ""
	if payload.Turn.Error != nil {
		message = payload.Turn.Error.Message
	}
	return payload.Turn.Status, message, true
}

func mustObject(raw []byte) map[string]any {
	result := make(map[string]any)
	_ = json.Unmarshal(raw, &result)
	return result
}

func extractStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func numberAsInt(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	}
	return 0
}

func numberAsInt64(value any) int64 {
	switch number := value.(type) {
	case float64:
		return int64(number)
	case int64:
		return number
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
