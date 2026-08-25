package codex

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrSessionLimit = errors.New("the Codex session limit has been reached")

// AgentSession is a logical execution session. It is deliberately not an OS
// process: all agents share one authenticated App Server and get isolated
// threads/turns inside it.
type AgentSession struct {
	ID             string
	AgentID        string
	ThreadID       string
	StartedAt      time.Time
	LastActivityAt time.Time
}

type SessionStats struct {
	Active int
	Max    int
}

type SessionManager struct {
	mu          sync.Mutex
	sessions    map[string]AgentSession
	active      int
	maxActive   int
	nextSession atomic.Uint64
}

func NewSessionManager(maxActive int) *SessionManager {
	if maxActive <= 0 {
		maxActive = 128
	}
	return &SessionManager{sessions: make(map[string]AgentSession), maxActive: maxActive}
}

func (m *SessionManager) Open(agentID, threadID string) (AgentSession, error) {
	if m == nil {
		return AgentSession{}, errors.New("session manager is not configured")
	}
	if strings.TrimSpace(agentID) == "" {
		agentID = "anonymous"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active >= m.maxActive {
		return AgentSession{}, fmt.Errorf("%w (%d active sessions)", ErrSessionLimit, m.maxActive)
	}
	now := time.Now().UTC()
	session := AgentSession{
		ID:             fmt.Sprintf("agent-session-%d", m.nextSession.Add(1)),
		AgentID:        agentID,
		ThreadID:       threadID,
		StartedAt:      now,
		LastActivityAt: now,
	}
	m.sessions[session.ID] = session
	m.active++
	return session, nil
}

func (m *SessionManager) Touch(sessionID, threadID string) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[sessionID]
	if !ok {
		return
	}
	if threadID != "" {
		session.ThreadID = threadID
	}
	session.LastActivityAt = time.Now().UTC()
	m.sessions[sessionID] = session
}

func (m *SessionManager) Close(sessionID string) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[sessionID]; !ok {
		return
	}
	delete(m.sessions, sessionID)
	if m.active > 0 {
		m.active--
	}
}

func (m *SessionManager) ActiveCount() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

func (m *SessionManager) MaxActive() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxActive
}

func (m *SessionManager) Stats() SessionStats {
	if m == nil {
		return SessionStats{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return SessionStats{Active: m.active, Max: m.maxActive}
}
