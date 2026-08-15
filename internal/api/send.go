package api

import (
	"log"
	"net/http"
	"strconv"

	"github.com/nc1107/check-in-relay/internal/fcm"
	"github.com/nc1107/check-in-relay/internal/keys"
)

type sendReq struct {
	Messages []sendMessage `json:"messages"`
}

type sendMessage struct {
	Token string            `json:"token"`
	Title string            `json:"title"`
	Body  string            `json:"body"`
	Data  map[string]string `json:"data"`

	// CollapseID is optional: a server sets it so several notifications about the same
	// event (one check-in cross-posted to several groups) show up as one on the device.
	// Bodies decode with DisallowUnknownFields, so this field has to exist here before any
	// server starts sending it, and omitting it has to keep working for servers that never
	// will.
	CollapseID string `json:"collapseId"`
}

type sendResp struct {
	Results []fcm.Result `json:"results"`
}

// handleSend forwards a batch of notifications to FCM on behalf of a registered server. The
// caller authenticates with its registration key, is rate-limited per key, and gets back one
// result per token so it can prune the ones FCM reports as dead. The relay never logs the
// tokens or the notification text - only counts and the key id.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	plain := bearerToken(r)
	if plain == "" {
		writeErr(w, http.StatusUnauthorized, "missing bearer key")
		return
	}
	k, err := s.keys.Verify(r.Context(), plain)
	if err != nil {
		if err == keys.ErrNotFound {
			writeErr(w, http.StatusUnauthorized, "invalid or revoked key")
			return
		}
		log.Printf("relay: verify key: %v", err)
		writeErr(w, http.StatusInternalServerError, "server error")
		return
	}
	if !s.sendLim.Allow(strconv.FormatInt(k.ID, 10)) {
		writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	var req sendReq
	if err := decodeJSON(w, r, &req, 1<<20); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(req.Messages) > s.cfg.MaxMessages {
		writeErr(w, http.StatusBadRequest, "too many messages in one request")
		return
	}

	msgs := make([]fcm.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Token == "" {
			continue
		}
		msgs = append(msgs, fcm.Message{
			Token:      m.Token,
			Title:      m.Title,
			Body:       m.Body,
			Data:       m.Data,
			CollapseID: m.CollapseID,
		})
	}
	if len(msgs) == 0 {
		writeJSON(w, http.StatusOK, sendResp{Results: []fcm.Result{}})
		return
	}

	results := s.fcm.Send(r.Context(), msgs)
	delivered, unregistered, failed := tally(results)
	log.Printf("relay: send key=%d n=%d delivered=%d unregistered=%d error=%d",
		k.ID, len(msgs), delivered, unregistered, failed)
	writeJSON(w, http.StatusOK, sendResp{Results: results})
}
