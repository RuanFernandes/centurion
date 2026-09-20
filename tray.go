package main

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/store"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const (
	trayTooltipLimit = 127
	trayRunCacheSize = 32
)

type trayRunState struct {
	id           string
	workflowID   string
	workflowName string
	status       string
	currentNode  string
	lastSeen     time.Time
}

type trayController struct {
	app       *application.App
	tray      *application.SystemTray
	window    *application.WebviewWindow
	dataStore *store.Store

	exitRequested atomic.Bool
	mu            sync.RWMutex
	runs          map[string]trayRunState
	workflowNames map[string]string
	authStatus    string
}

func newTrayController(app *application.App, window *application.WebviewWindow, dataStore *store.Store, icon []byte) *trayController {
	controller := &trayController{
		app:           app,
		window:        window,
		dataStore:     dataStore,
		runs:          make(map[string]trayRunState),
		workflowNames: make(map[string]string),
	}

	tray := app.SystemTray.New()
	controller.tray = tray

	menu := app.Menu.New()
	menu.Add("Open Centurion").OnClick(func(*application.Context) {
		tray.ShowWindow()
	})
	menu.AddSeparator()
	menu.Add("Exit Centurion").OnClick(func(*application.Context) {
		controller.exitRequested.Store(true)
		app.Quit()
	})

	// A left click always opens the main window. The window itself is hidden by
	// the close hook, so the background process and Codex session remain alive.
	tray.AttachWindow(window).
		WindowOffset(5).
		WindowDebounce(250 * time.Millisecond).
		SetMenu(menu).
		SetIcon(icon)
	tray.OnClick(func() {
		tray.ShowWindow()
	})
	tray.OnRightClick(func() {
		controller.refreshTooltip()
		tray.OpenMenu()
	})
	tray.OnMouseEnter(func() {
		controller.refreshTooltip()
	})

	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if controller.exitRequested.Load() {
			return
		}
		window.Hide()
		event.Cancel()
	})

	controller.loadRecentRuns()
	controller.refreshTooltip()
	return controller
}

func (t *trayController) loadRecentRuns() {
	if t.dataStore == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	runs, err := t.dataStore.ListRuns(ctx, model.RunFilter{Limit: trayRunCacheSize})
	if err != nil {
		return
	}
	for _, run := range runs {
		t.rememberRun(run)
	}
}

func (t *trayController) updateFromEvent(name string, payload any) {
	switch name {
	case "auth.updated":
		if auth, ok := trayAuthState(payload); ok {
			t.mu.Lock()
			t.authStatus = auth.Status
			t.mu.Unlock()
		}
	case "run.created":
		if run, ok := trayRun(payload); ok {
			t.rememberRun(run)
		}
	case "run.updated":
		if update, ok := trayRunUpdate(payload); ok {
			t.updateRun(update.RunID, update.RunStatus, update.Event, "")
		}
	case "run.event":
		if event, ok := trayRunEvent(payload); ok {
			t.updateFromRunEvent(event)
		}
	}
	t.refreshTooltip()
}

func (t *trayController) rememberRun(run model.Run) {
	if strings.TrimSpace(run.ID) == "" {
		return
	}
	workflowName := t.workflowName(run.WorkflowID)
	lastSeen := time.Now().UTC()
	if parsed, err := time.Parse(time.RFC3339Nano, run.UpdatedAt); err == nil {
		lastSeen = parsed
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.runs[run.ID]; ok {
		// A very fast run can emit its terminal event before the run.created
		// notification reaches the UI. Never regress that state to running.
		if isTerminalTrayStatus(existing.status) && !isTerminalTrayStatus(run.Status) {
			if existing.workflowName == "" {
				existing.workflowName = workflowName
			}
			t.runs[run.ID] = existing
			return
		}
	}
	t.runs[run.ID] = trayRunState{
		id:           run.ID,
		workflowID:   run.WorkflowID,
		workflowName: workflowName,
		status:       run.Status,
		currentNode:  run.CurrentNodeID,
		lastSeen:     lastSeen,
	}
	t.pruneRunsLocked()
}

func (t *trayController) updateFromRunEvent(event model.RunEvent) {
	status := trayStatusFromEvent(event)
	workflowID := trayString(event.Data, "workflowID")
	t.updateRun(event.RunID, status, event.Type, workflowID)

	if event.NodeID == "" {
		return
	}
	t.mu.Lock()
	if current, ok := t.runs[event.RunID]; ok {
		current.currentNode = event.NodeID
		current.lastSeen = time.Now().UTC()
		t.runs[event.RunID] = current
	}
	t.mu.Unlock()
}

func (t *trayController) updateRun(runID, status, eventType, workflowID string) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	status = trayStatusFromUpdate(status, eventType)
	if status == "" && workflowID == "" {
		return
	}

	t.mu.Lock()
	current := t.runs[runID]
	if current.id == "" {
		current.id = runID
	}
	if workflowID != "" {
		current.workflowID = workflowID
	}
	if current.workflowName == "" && current.workflowID != "" {
		current.workflowName = t.workflowNameLocked(current.workflowID)
	}
	if status != "" && !(isTerminalTrayStatus(current.status) && !isTerminalTrayStatus(status)) {
		current.status = status
	}
	current.lastSeen = time.Now().UTC()
	t.runs[runID] = current
	t.pruneRunsLocked()
	t.mu.Unlock()
}

func (t *trayController) workflowName(workflowID string) string {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return "Workflow"
	}

	t.mu.RLock()
	if name := t.workflowNames[workflowID]; name != "" {
		t.mu.RUnlock()
		return name
	}
	t.mu.RUnlock()

	name := ""
	if t.dataStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		workflow, err := t.dataStore.GetWorkflow(ctx, workflowID)
		cancel()
		if err == nil {
			name = strings.TrimSpace(workflow.Name)
		}
	}
	if name == "" {
		name = shortTrayID(workflowID)
	}

	t.mu.Lock()
	t.workflowNames[workflowID] = name
	t.mu.Unlock()
	return name
}

// workflowNameLocked is used while t.mu is already held. It deliberately
// returns a stable fallback instead of querying SQLite under the state lock.
func (t *trayController) workflowNameLocked(workflowID string) string {
	if name := t.workflowNames[workflowID]; name != "" {
		return name
	}
	return shortTrayID(workflowID)
}

func (t *trayController) pruneRunsLocked() {
	if len(t.runs) <= trayRunCacheSize {
		return
	}
	var oldestID string
	var oldest time.Time
	for id, run := range t.runs {
		if oldestID == "" || run.lastSeen.Before(oldest) {
			oldestID = id
			oldest = run.lastSeen
		}
	}
	if oldestID != "" {
		delete(t.runs, oldestID)
	}
}

func (t *trayController) refreshTooltip() {
	if t.tray == nil {
		return
	}
	t.tray.SetTooltip(t.tooltip())
}

func (t *trayController) tooltip() string {
	t.mu.RLock()
	runs := make([]trayRunState, 0, len(t.runs))
	for _, run := range t.runs {
		runs = append(runs, run)
	}
	authStatus := t.authStatus
	t.mu.RUnlock()
	return trayTooltip(runs, authStatus)
}

func trayTooltip(runs []trayRunState, authStatus string) string {
	active := make([]trayRunState, 0, len(runs))
	var latest *trayRunState
	for index := range runs {
		run := runs[index]
		if latest == nil || run.lastSeen.After(latest.lastSeen) {
			copy := run
			latest = &copy
		}
		if isActiveTrayStatus(run.status) {
			active = append(active, run)
		}
	}
	if len(active) > 0 {
		current := latestTrayRun(active)
		label := current.workflowName
		if label == "" {
			label = shortTrayID(current.workflowID)
		}
		if len(active) == 1 {
			return limitTrayTooltip("Centurion | " + label + " | " + trayStatusLabel(current.status))
		}
		return limitTrayTooltip("Centurion | " + strconv.Itoa(len(active)) + " workflows active | " + label + " | " + trayStatusLabel(current.status))
	}
	if latest != nil {
		label := latest.workflowName
		if label == "" {
			label = shortTrayID(latest.workflowID)
		}
		return limitTrayTooltip("Centurion | " + label + " | " + trayStatusLabel(latest.status))
	}

	switch authStatus {
	case model.AuthStatusLoggedIn:
		return "Centurion | Codex connected | No active workflow"
	case model.AuthStatusOffline, model.AuthStatusError:
		return "Centurion | Codex offline | No active workflow"
	case model.AuthStatusChecking:
		return "Centurion | Starting Codex | No active workflow"
	case model.AuthStatusLoggedOut:
		return "Centurion | Codex sign-in required"
	default:
		return "Centurion | No active workflow"
	}
}

func latestTrayRun(runs []trayRunState) trayRunState {
	latest := runs[0]
	for _, run := range runs[1:] {
		if run.lastSeen.After(latest.lastSeen) {
			latest = run
		}
	}
	return latest
}

func trayStatusFromEvent(event model.RunEvent) string {
	if status := trayString(event.Data, "status"); status != "" {
		return normalizeTrayStatus(status)
	}
	switch event.Type {
	case "run.created", "run.resumed":
		return model.RunStatusRunning
	case "run.paused":
		return model.RunStatusPaused
	case "run.queued":
		return model.RunStatusQueued
	case "run.completed":
		return model.RunStatusCompleted
	case "run.blocked":
		return model.RunStatusBlocked
	case "run.failed":
		return model.RunStatusFailed
	case "run.canceled":
		return model.RunStatusCanceled
	case "run.interrupted":
		return model.RunStatusInterrupted
	default:
		return ""
	}
}

func trayStatusFromUpdate(status, eventType string) string {
	if normalized := normalizeTrayStatus(status); normalized != "" {
		return normalized
	}
	if strings.HasPrefix(eventType, "run.") {
		return trayStatusFromEvent(model.RunEvent{Type: eventType})
	}
	return ""
}

func normalizeTrayStatus(status string) string {
	switch strings.TrimSpace(status) {
	case model.RunStatusQueued, model.RunStatusRunning, model.RunStatusPaused, model.RunStatusWaitingApproval,
		model.RunStatusCompleted, model.RunStatusBlocked, model.RunStatusFailed, model.RunStatusCanceled, model.RunStatusInterrupted:
		return strings.TrimSpace(status)
	default:
		return ""
	}
}

func isActiveTrayStatus(status string) bool {
	switch status {
	case model.RunStatusQueued, model.RunStatusRunning, model.RunStatusPaused, model.RunStatusWaitingApproval:
		return true
	default:
		return false
	}
}

func isTerminalTrayStatus(status string) bool {
	switch status {
	case model.RunStatusCompleted, model.RunStatusBlocked, model.RunStatusFailed, model.RunStatusCanceled, model.RunStatusInterrupted:
		return true
	default:
		return false
	}
}

func trayStatusLabel(status string) string {
	switch status {
	case model.RunStatusQueued:
		return "Queued"
	case model.RunStatusRunning:
		return "Running"
	case model.RunStatusPaused:
		return "Paused"
	case model.RunStatusWaitingApproval:
		return "Approval needed"
	case model.RunStatusCompleted:
		return "Completed"
	case model.RunStatusBlocked:
		return "Blocked"
	case model.RunStatusFailed:
		return "Failed"
	case model.RunStatusCanceled:
		return "Canceled"
	case model.RunStatusInterrupted:
		return "Interrupted"
	default:
		return "Unknown"
	}
}

func shortTrayID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Workflow"
	}
	if len(value) > 18 {
		return value[:8] + "..."
	}
	return value
}

func limitTrayTooltip(value string) string {
	if len(utf16.Encode([]rune(value))) <= trayTooltipLimit {
		return value
	}

	const suffix = "..."
	maxUnits := trayTooltipLimit - len(utf16.Encode([]rune(suffix)))
	var builder strings.Builder
	units := 0
	for _, character := range value {
		characterUnits := utf16.RuneLen(character)
		if units+characterUnits > maxUnits {
			break
		}
		builder.WriteRune(character)
		units += characterUnits
	}
	return strings.TrimSpace(builder.String()) + suffix
}

func trayString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func trayRun(value any) (model.Run, bool) {
	switch run := value.(type) {
	case model.Run:
		return run, true
	case *model.Run:
		if run != nil {
			return *run, true
		}
	}
	return model.Run{}, false
}

func trayRunUpdate(value any) (model.RunUpdateEvent, bool) {
	switch update := value.(type) {
	case model.RunUpdateEvent:
		return update, true
	case *model.RunUpdateEvent:
		if update != nil {
			return *update, true
		}
	}
	return model.RunUpdateEvent{}, false
}

func trayRunEvent(value any) (model.RunEvent, bool) {
	switch event := value.(type) {
	case model.RunEvent:
		return event, true
	case *model.RunEvent:
		if event != nil {
			return *event, true
		}
	}
	return model.RunEvent{}, false
}

func trayAuthState(value any) (model.AuthState, bool) {
	switch auth := value.(type) {
	case model.AuthState:
		return auth, true
	case *model.AuthState:
		if auth != nil {
			return *auth, true
		}
	}
	return model.AuthState{}, false
}
