package main

import (
	"context"
	"embed"
	"log"
	"os"
	"strings"
	"time"

	"github.com/RuanFernandes/centurion/internal/model"
	"github.com/RuanFernandes/centurion/internal/scheduler"
	"github.com/RuanFernandes/centurion/internal/store"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails embeds the Vite production bundle in the desktop binary.
//
//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[model.AuthState]("auth.updated")
	application.RegisterEvent[model.ModelsUpdatedEvent]("models.updated")
	application.RegisterEvent[model.MCPStatusEvent]("mcp.status")
	application.RegisterEvent[model.Run]("run.created")
	application.RegisterEvent[model.RunUpdateEvent]("run.updated")
	application.RegisterEvent[model.RunEvent]("run.event")
	application.RegisterEvent[model.ApprovalRequest]("approval.requested")
	application.RegisterEvent[model.ApprovalDecision]("approval.resolved")
	application.RegisterEvent[model.SchedulerEvent]("scheduler.updated")
	application.RegisterEvent[model.AgentStateEvent]("office.agent.state")
	application.RegisterEvent[model.Project]("project.updated")
	application.RegisterEvent[model.CodexNotification]("codex.notification")
}

func main() {
	scheduleID, scheduled := scheduledRunID()
	releaseInstance, acquired, err := scheduler.AcquireInstance("Global\\Centurion")
	if err != nil {
		log.Fatal(err)
	}
	if !acquired {
		if scheduled {
			if err := scheduler.ForwardScheduledRun(scheduleID); err != nil {
				log.Printf("forward scheduled run: %v", err)
			}
		}
		log.Print("Centurion is already running; the schedule will be handled by the existing instance.")
		return
	}
	defer releaseInstance()

	databasePath, err := store.DefaultPath()
	if err != nil {
		log.Fatal(err)
	}
	dataStore, err := store.Open(databasePath)
	if err != nil {
		log.Fatal(err)
	}
	defer dataStore.Close()

	service := NewAppService(dataStore)
	if scheduled {
		runScheduled(dataStore, service, scheduleID)
		return
	}
	app := application.New(application.Options{
		Name:        "Centurion",
		Description: "Local-first Codex agent orchestration workspace",
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	service.setApp(app)
	service.setEmitter(func(name string, payload any) {
		app.Event.Emit(name, payload)
	})
	go service.Connect(context.Background())
	if stopIPC, ipcErr := scheduler.StartIPC(context.Background(), func(id string) {
		go triggerScheduledRun(dataStore, service, id)
	}); ipcErr == nil {
		defer stopIPC()
	} else {
		log.Printf("scheduler IPC unavailable: %v", ipcErr)
	}

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Centurion · Agent Command Center",
		Width:            1440,
		Height:           900,
		MinWidth:         1080,
		MinHeight:        680,
		Frameless:        true,
		BackgroundColour: application.NewRGB(9, 12, 20),
		URL:              "/",
	})

	if err := app.Run(); err != nil {
		service.Close()
		log.Fatal(err)
	}
	service.Close()
}

func scheduledRunID() (string, bool) {
	for index, argument := range os.Args {
		if argument == "--scheduled-run" && index+1 < len(os.Args) && strings.TrimSpace(os.Args[index+1]) != "" {
			return strings.TrimSpace(os.Args[index+1]), true
		}
	}
	return "", false
}

func runScheduled(dataStore *store.Store, service *AppService, scheduleID string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go service.Connect(ctx)

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		state := service.GetAuthState()
		if state.Status == model.AuthStatusLoggedIn {
			break
		}
		if state.Status == model.AuthStatusOffline || state.Status == model.AuthStatusError {
			log.Printf("scheduled run waiting for Codex: %s", state.Error)
		}
		time.Sleep(500 * time.Millisecond)
	}

	schedules, err := dataStore.ListSchedules(ctx)
	if err != nil {
		log.Printf("list schedules: %v", err)
		service.Close()
		return
	}
	var selected *model.Schedule
	for index := range schedules {
		if schedules[index].ID == scheduleID {
			selected = &schedules[index]
			break
		}
	}
	if selected == nil || !selected.Enabled {
		log.Printf("schedule %s not found or disabled", scheduleID)
		service.Close()
		return
	}
	run, err := service.StartRun(selected.WorkflowID, map[string]any{"trigger": "schedule", "scheduleID": scheduleID})
	if err != nil {
		log.Printf("start scheduled run: %v", err)
		service.Close()
		return
	}
	log.Printf("scheduled run started: %s", run.ID)
	for {
		current, getErr := service.GetRun(run.ID)
		if getErr != nil {
			log.Printf("read scheduled run: %v", getErr)
			break
		}
		switch current.Status {
		case model.RunStatusCompleted, model.RunStatusFailed, model.RunStatusCanceled, model.RunStatusInterrupted:
			log.Printf("scheduled run finished: %s", current.Status)
			service.Close()
			return
		}
		time.Sleep(time.Second)
	}
	service.Close()
}

func triggerScheduledRun(dataStore *store.Store, service *AppService, scheduleID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) && service.GetAuthState().Status != model.AuthStatusLoggedIn {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	schedules, err := dataStore.ListSchedules(ctx)
	if err != nil {
		log.Printf("list schedules from IPC: %v", err)
		return
	}
	for _, schedule := range schedules {
		if schedule.ID != scheduleID || !schedule.Enabled {
			continue
		}
		run, startErr := service.StartRun(schedule.WorkflowID, map[string]any{"trigger": "schedule", "scheduleID": scheduleID})
		if startErr != nil {
			log.Printf("start scheduled run from IPC: %v", startErr)
			return
		}
		log.Printf("scheduled run forwarded to main instance: %s", run.ID)
		return
	}
	log.Printf("schedule %s not found or disabled", scheduleID)
}
