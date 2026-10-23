package main

// Live updates: subscribe to /events (SSE); on failure fall back to polling
// /graph with If-None-Match, retrying SSE on every poll. Changed rows flash.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/coulof/ovn-topology/pkg/api"
)

type liveState int

const (
	liveConnecting liveState = iota
	liveSSE
	livePolling
)

const pollEvery = 10 * time.Second

type sseStream struct {
	body io.ReadCloser
	sc   *bufio.Scanner
}

type liveEventMsg struct {
	ev     api.GraphEvent
	stream *sseStream
}

type liveErrMsg struct{ err error }

type pollTickMsg struct{}

type flashTickMsg struct{}

func (m model) connectLiveCmd() tea.Cmd {
	daemon := m.daemonURL
	return func() tea.Msg {
		req, err := http.NewRequest(http.MethodGet, apiURL(daemon, "/events", nil), nil)
		if err != nil {
			return liveErrMsg{err}
		}
		req.Header.Set("Accept", "text/event-stream")
		resp, err := http.DefaultClient.Do(req) // no timeout: long-lived stream
		if err != nil {
			return liveErrMsg{err}
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return liveErrMsg{fmt.Errorf("events: HTTP %d", resp.StatusCode)}
		}
		return readSSE(&sseStream{body: resp.Body, sc: bufio.NewScanner(resp.Body)})()
	}
}

func readSSE(s *sseStream) tea.Cmd {
	return func() tea.Msg {
		for s.sc.Scan() {
			line := s.sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var ev api.GraphEvent
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev) == nil {
				return liveEventMsg{ev: ev, stream: s}
			}
		}
		_ = s.body.Close()
		err := s.sc.Err()
		if err == nil {
			err = io.EOF
		}
		return liveErrMsg{err}
	}
}

func pollTick() tea.Cmd { return tea.Tick(pollEvery, func(time.Time) tea.Msg { return pollTickMsg{} }) }

func flashTick() tea.Cmd {
	return tea.Tick(flashFor, func(time.Time) tea.Msg { return flashTickMsg{} })
}

func (m model) updateLive(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case liveEventMsg:
		m.live = liveSSE
		next := readSSE(msg.stream)
		if m.livePaused || (m.graphResp != nil && msg.ev.Revision == m.graphResp.Revision) {
			return m, next
		}
		m.pendingIDs = append(m.pendingIDs, msg.ev.Changed...)
		return m, tea.Batch(next, m.fetchGraphCmd())
	case liveErrMsg:
		wasPolling := m.live == livePolling
		m.live = livePolling
		if !wasPolling {
			m.setFlash("live stream unavailable ("+msg.err.Error()+"); polling every "+pollEvery.String(), true)
		}
		return m, pollTick()
	case pollTickMsg:
		if m.live != livePolling {
			return m, nil
		}
		m.live = liveConnecting
		cmds := []tea.Cmd{m.connectLiveCmd()}
		if !m.livePaused {
			cmds = append(cmds, m.fetchGraphCmd())
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

// markChanged flashes rows whose content differs from the previous tree.
// New objects only flash after the initial load so the first frames stay calm.
func (m *model) markChanged(next *xtree) {
	if m.tree == nil || m.graphResp == nil {
		return
	}
	now := time.Now()
	hit := false
	for id, n := range next.byID {
		old, ok := m.tree.byID[id]
		switch {
		case ok && old.sig() != n.sig() && m.graphResp.Revision > m.baseRevision:
			m.changed[id] = now
			hit = true
		case !ok && m.graphResp.Revision > m.baseRevision && n.kind != kGroup:
			m.changed[id] = now
			hit = true
		}
	}
	if hit {
		m.needFlashTick = true
	}
}

func (m *model) setFlash(s string, isErr bool) {
	m.flash, m.flashAt, m.flashErr = s, time.Now(), isErr
}
