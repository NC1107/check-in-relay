package fcm

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// testSender points a Sender at a fake FCM so Send can be exercised without credentials.
func testSender(endpoint string) *Sender {
	return &Sender{
		tokens:    oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test"}),
		projectID: "check-in-48fdc",
		http:      &http.Client{Timeout: 5 * time.Second},
		endpoint:  endpoint,
	}
}

func TestErrorCode(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"unregistered", `{"error":{"code":404,"status":"NOT_FOUND","details":[
			{"errorCode":"UNREGISTERED"}]}}`, "UNREGISTERED"},
		{"falls back to status", `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`, "UNAUTHENTICATED"},
		{"not json", `<html>502</html>`, ""},
		{"empty", ``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorCode([]byte(tt.body)); got != tt.want {
				t.Errorf("errorCode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Send must map each FCM response to the right per-token status, in input order, so the
// caller can prune exactly the dead tokens.
func TestSendMapsPerTokenStatus(t *testing.T) {
	fcm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(body), "dead"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`))
		case strings.Contains(string(body), "boom"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"status":"INTERNAL"}}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer fcm.Close()

	s := testSender(fcm.URL)
	results := s.Send(context.Background(), []Message{
		{Token: "good"}, {Token: "dead"}, {Token: "boom"},
	})

	want := []Result{
		{Token: "good", Status: StatusDelivered},
		{Token: "dead", Status: StatusUnregistered},
		{Token: "boom", Status: StatusError},
	}
	if len(results) != len(want) {
		t.Fatalf("got %d results, want %d", len(results), len(want))
	}
	for i := range want {
		if results[i] != want[i] {
			t.Errorf("result %d = %+v, want %+v", i, results[i], want[i])
		}
	}
}

// A notification's title and body must never reach the logs, whatever FCM answers.
func TestSendNeverLogsContent(t *testing.T) {
	fcm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"status":"INTERNAL"}}`))
	}))
	defer fcm.Close()

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(nil) })

	s := testSender(fcm.URL)
	s.Send(context.Background(), []Message{{Token: "tok", Title: "Alice checked in", Body: "at the gym"}})

	for _, secret := range []string{"Alice", "gym", "tok"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log leaked %q:\n%s", secret, buf.String())
		}
	}
}
