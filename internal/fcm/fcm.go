// Package fcm forwards notifications to Firebase Cloud Messaging (FCM HTTP v1), which
// delivers to Android directly and to iOS through APNs. It talks to the REST API directly
// with an OAuth2 token minted from the service account, avoiding the heavyweight Firebase
// Admin SDK.
//
// This is a deliberate copy of the send path in the Check-In server's internal/push
// package: Go forbids importing another module's internal/, and keeping a copy leaves the
// relay independent. The one addition is a per-token Result so the caller can prune tokens
// FCM reports as dead.
package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	scope    = "https://www.googleapis.com/auth/firebase.messaging"
	endpoint = "https://fcm.googleapis.com"
)

// Message is a single notification bound for one device token.
type Message struct {
	Token string
	Title string
	Body  string
	Data  map[string]string
}

// Status is the delivery outcome for one token.
type Status string

const (
	// StatusDelivered means FCM accepted the message.
	StatusDelivered Status = "delivered"
	// StatusUnregistered means FCM rejected the token as no longer valid; the caller
	// should delete it from its own store.
	StatusUnregistered Status = "unregistered"
	// StatusError means the send failed for some other reason and may be worth a retry.
	StatusError Status = "error"
)

// Result pairs an input token with its delivery outcome.
type Result struct {
	Token  string `json:"token"`
	Status Status `json:"status"`
}

// Sender holds the FCM credentials and posts messages to the REST API.
type Sender struct {
	tokens    oauth2.TokenSource
	projectID string
	http      *http.Client
	endpoint  string
}

// New builds a Sender from a Firebase service-account JSON.
func New(ctx context.Context, credentialsJSON []byte) (*Sender, error) {
	creds, err := google.CredentialsFromJSON(ctx, credentialsJSON, scope)
	if err != nil {
		return nil, fmt.Errorf("parse FCM credentials: %w", err)
	}
	if creds.ProjectID == "" {
		return nil, fmt.Errorf("FCM credentials missing project_id")
	}
	return &Sender{
		tokens:    creds.TokenSource,
		projectID: creds.ProjectID,
		http:      &http.Client{Timeout: 15 * time.Second},
		endpoint:  endpoint,
	}, nil
}

// ProjectID is the Firebase project these notifications are sent through.
func (s *Sender) ProjectID() string { return s.projectID }

// Send delivers every message and returns one Result per input in the same order (FCM v1
// has no multicast, so it is one request per token). It never logs the token or the message
// content; the caller decides what to record from the returned statuses.
func (s *Sender) Send(ctx context.Context, msgs []Message) []Result {
	results := make([]Result, len(msgs))
	tok, err := s.tokens.Token()
	if err != nil {
		for i, m := range msgs {
			results[i] = Result{Token: m.Token, Status: StatusError}
		}
		return results
	}
	url := fmt.Sprintf("%s/v1/projects/%s/messages:send", s.endpoint, s.projectID)
	for i, m := range msgs {
		results[i] = Result{Token: m.Token, Status: s.sendOne(ctx, url, tok.AccessToken, m)}
	}
	return results
}

func (s *Sender) sendOne(ctx context.Context, url, access string, m Message) Status {
	payload, _ := json.Marshal(map[string]any{
		"message": map[string]any{
			"token":        m.Token,
			"notification": map[string]string{"title": m.Title, "body": m.Body},
			"data":         m.Data,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return StatusError
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return StatusError
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		return StatusDelivered
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if errorCode(raw) == "UNREGISTERED" {
		return StatusUnregistered
	}
	return StatusError
}

// errorCode pulls FCM v1's machine-readable code out of an error response, preferring the
// specific code in details (e.g. UNREGISTERED) over the generic status. Returns "" when the
// body isn't the shape we expect.
func errorCode(body []byte) string {
	var resp struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	for _, d := range resp.Error.Details {
		if d.ErrorCode != "" {
			return d.ErrorCode
		}
	}
	return resp.Error.Status
}
