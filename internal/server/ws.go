package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type claimHub struct {
	mu    sync.Mutex
	conns map[string]map[*websocket.Conn]struct{}
}

func newClaimHub() *claimHub {
	return &claimHub{conns: make(map[string]map[*websocket.Conn]struct{})}
}

func (h *claimHub) add(claimID string, conn *websocket.Conn) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[claimID] == nil {
		h.conns[claimID] = make(map[*websocket.Conn]struct{})
	}
	h.conns[claimID][conn] = struct{}{}
	return len(h.conns[claimID])
}

func (h *claimHub) remove(claimID string, conn *websocket.Conn) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if values := h.conns[claimID]; values != nil {
		delete(values, conn)
		if len(values) == 0 {
			delete(h.conns, claimID)
		}
		return len(values)
	}
	return 0
}

func (h *claimHub) closeClaim(claimID string) {
	h.mu.Lock()
	values := h.conns[claimID]
	delete(h.conns, claimID)
	h.mu.Unlock()
	for conn := range values {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "内容已销毁"), time.Now().Add(time.Second))
		_ = conn.Close()
	}
}

func (h *claimHub) closeAll() {
	h.mu.Lock()
	all := h.conns
	h.conns = make(map[string]map[*websocket.Conn]struct{})
	h.mu.Unlock()
	for _, values := range all {
		for conn := range values {
			_ = conn.Close()
		}
	}
}

func (a *App) handleClaimWebSocket(w http.ResponseWriter, r *http.Request) {
	token := wsClaimToken(r)
	claim, err := a.claimByID(r.PathValue("claimId"))
	if err != nil || token == "" || !constantEqual(claim.TokenHash, tokenHash(token)) || claim.State != "active" || claim.ExpiresAt <= nowUnix() {
		writeError(w, http.StatusUnauthorized, "领取会话无效")
		return
	}
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		Subprotocols:     []string{"flashshare.v1"},
		CheckOrigin: func(req *http.Request) bool {
			return sameOrigin(req)
		},
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(1024)
	a.hub.add(claim.ID, conn)
	a.extendClaimLease(claim.ID)
	conn.SetPongHandler(func(string) error {
		return a.extendClaimLease(claim.ID)
	})
	conn.SetCloseHandler(func(code int, text string) error {
		return nil
	})
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		defer close(done)
		for {
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(2*time.Second)); err != nil {
				return
			}
			select {
			case <-ticker.C:
			case <-done:
				return
			}
		}
	}()
	for {
		conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if err := a.extendClaimLease(claim.ID); err != nil || nowUnix() >= claim.ExpiresAt {
			break
		}
	}
	remaining := a.hub.remove(claim.ID, conn)
	_ = conn.Close()
	if remaining == 0 {
		_, _ = a.db.Exec(`UPDATE claims SET lease_expires_at=? WHERE id=? AND state='active'`, nowUnix()+15, claim.ID)
	}
}

func (a *App) extendClaimLease(claimID string) error {
	_, err := a.db.Exec(`UPDATE claims SET lease_expires_at=? WHERE id=? AND state='active' AND expires_at>?`, nowUnix()+15, claimID, nowUnix())
	return err
}

func wsClaimToken(r *http.Request) string {
	for _, protocol := range websocket.Subprotocols(r) {
		if protocol != "flashshare.v1" && strings.TrimSpace(protocol) != "" {
			return strings.TrimSpace(protocol)
		}
	}
	return ""
}
