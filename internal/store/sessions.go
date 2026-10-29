// Package store holds the gateway's small JSON-backed state: canvas sessions and
// the three user preference files (skins, workflow visibility, main models).
package store

import (
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/util"
)

// Sessions tracks how many canvas tabs are open.
//
// The canvas sends heartbeats; a tab that stops sending is considered gone after
// staleMS. This exists so the gateway can tell "idle" from "nobody is looking",
// which governs whether it is safe to free VRAM.
type Sessions struct {
	staleMS time.Duration

	mu        sync.Mutex
	sessions  map[string]time.Time
	seenAt    time.Time
	emptyFrom time.Time
	closedAt  time.Time
	startedAt time.Time
}

// NewSessions builds a tracker with the given staleness window.
func NewSessions(staleMS time.Duration) *Sessions {
	return &Sessions{
		staleMS:   staleMS,
		sessions:  map[string]time.Time{},
		startedAt: time.Now(),
	}
}

// SessionStatus is the wire projection returned by /api/session/*.
type SessionStatus struct {
	Count      int     `json:"count"`
	Seen       bool    `json:"seen"`
	IdleMs     int64   `json:"idleMs"`
	StaleMs    int64   `json:"staleMs"`
	LastSeenAt *string `json:"lastSeenAt"`
	StartedAt  string  `json:"startedAt"`
	ClosedAt   int64   `json:"closedAt"`
}

// pruneLocked drops sessions that stopped heartbeating.
func (s *Sessions) pruneLocked() {
	now := time.Now()
	var lastRemoved time.Time
	for id, lastSeen := range s.sessions {
		if now.Sub(lastSeen) > s.staleMS {
			delete(s.sessions, id)
			if candidate := lastSeen.Add(s.staleMS); candidate.After(lastRemoved) {
				lastRemoved = candidate
			}
		}
	}
	if len(s.sessions) == 0 && s.emptyFrom.IsZero() {
		if !lastRemoved.IsZero() {
			s.emptyFrom = lastRemoved
		} else {
			s.emptyFrom = now
		}
	}
}

// Status reports the current session state.
func (s *Sessions) Status() SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pruneLocked()
	if len(s.sessions) > 0 {
		s.emptyFrom = time.Time{}
	}

	idleMs := int64(0)
	if len(s.sessions) == 0 {
		from := s.emptyFrom
		if from.IsZero() {
			from = time.Now()
		}
		idleMs = time.Since(from).Milliseconds()
	}

	status := SessionStatus{
		Count:     len(s.sessions),
		Seen:      !s.seenAt.IsZero(),
		IdleMs:    idleMs,
		StaleMs:   s.staleMS.Milliseconds(),
		StartedAt: util.IsoFromTime(s.startedAt),
	}
	if !s.closedAt.IsZero() {
		status.ClosedAt = s.closedAt.UnixMilli()
	}
	if !s.seenAt.IsZero() {
		value := util.IsoFromTime(s.seenAt)
		status.LastSeenAt = &value
	}
	return status
}

// Touch records a heartbeat from a session.
func (s *Sessions) Touch(id string) SessionStatus {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	if id != "" {
		now := time.Now()
		s.sessions[id] = now
		s.seenAt = now
		s.emptyFrom = time.Time{}
	}
	s.mu.Unlock()
	return s.Status()
}

// Close removes a session explicitly.
func (s *Sessions) Close(id string) SessionStatus {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	if id != "" {
		delete(s.sessions, id)
		s.closedAt = time.Now()
	}
	if len(s.sessions) == 0 && s.emptyFrom.IsZero() {
		s.emptyFrom = time.Now()
	}
	s.mu.Unlock()
	return s.Status()
}
