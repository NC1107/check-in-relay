package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nc1107/check-in-relay/internal/config"
	"github.com/nc1107/check-in-relay/internal/fcm"
	"github.com/nc1107/check-in-relay/internal/keys"
)

// fakeFCM records what it was asked to send and reports every token delivered.
type fakeFCM struct {
	got []fcm.Message
}

func (f *fakeFCM) Send(_ context.Context, msgs []fcm.Message) []fcm.Result {
	f.got = append(f.got, msgs...)
	out := make([]fcm.Result, len(msgs))
	for i, m := range msgs {
		out[i] = fcm.Result{Token: m.Token, Status: fcm.StatusDelivered}
	}
	return out
}

func newTestServer(t *testing.T, cfg config.Config) (*Server, *fakeFCM, *keys.Store) {
	t.Helper()
	store, err := keys.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(cfg, nil, store)
	fake := &fakeFCM{}
	srv.fcm = fake
	return srv, fake, store
}

// defaultCfg sets limits high enough not to interfere with tests that aren't about limits.
func defaultCfg() config.Config {
	return config.Config{
		MaxMessages:     500,
		RegisterPerHour: 1000,
		RegisterBurst:   1000,
		SendPerMinute:   100000,
		SendBurst:       100000,
	}
}

func do(h http.Handler, method, target, body, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestRegisterIssuesKey(t *testing.T) {
	srv, _, store := newTestServer(t, defaultCfg())
	rr := do(srv.Router(), http.MethodPost, "/v1/register", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("register = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	var resp registerResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(resp.Key, "ckr_") {
		t.Fatalf("key %q lacks ckr_ prefix", resp.Key)
	}
	k, err := store.Verify(context.Background(), resp.Key)
	if err != nil {
		t.Fatalf("issued key does not verify: %v", err)
	}
	if k.Label != "" {
		t.Errorf("a register with no publicUrl should have an empty label, got %q", k.Label)
	}
}

func TestRegisterRateLimited(t *testing.T) {
	cfg := defaultCfg()
	cfg.RegisterBurst = 1
	cfg.RegisterPerHour = 1
	srv, _, _ := newTestServer(t, cfg)
	h := srv.Router()

	if rr := do(h, http.MethodPost, "/v1/register", "", ""); rr.Code != http.StatusOK {
		t.Fatalf("first register = %d, want 200", rr.Code)
	}
	if rr := do(h, http.MethodPost, "/v1/register", "", ""); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second register = %d, want 429", rr.Code)
	}
}

func TestRegisterStoresClaimedURLAsLabel(t *testing.T) {
	srv, _, store := newTestServer(t, defaultCfg())
	body, _ := json.Marshal(registerReq{PublicURL: "https://alpha.example.com"})
	rr := do(srv.Router(), http.MethodPost, "/v1/register", string(body), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("register = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	var resp registerResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	k, err := store.Verify(context.Background(), resp.Key)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if k.Label != "https://alpha.example.com" {
		t.Errorf("label = %q, want the claimed public URL recorded", k.Label)
	}
}

func TestSendRequiresBearer(t *testing.T) {
	srv, _, _ := newTestServer(t, defaultCfg())
	if rr := do(srv.Router(), http.MethodPost, "/v1/send", `{"messages":[]}`, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("send without bearer = %d, want 401", rr.Code)
	}
}

func TestSendRejectsUnknownKey(t *testing.T) {
	srv, _, _ := newTestServer(t, defaultCfg())
	rr := do(srv.Router(), http.MethodPost, "/v1/send", `{"messages":[{"token":"t"}]}`, "ckr_nope")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("send with unknown key = %d, want 401", rr.Code)
	}
}

func TestSendDelivers(t *testing.T) {
	srv, fake, store := newTestServer(t, defaultCfg())
	key, _ := store.Issue(context.Background(), "")

	body := `{"messages":[{"token":"a","title":"hi"},{"token":"b","title":"hi"}]}`
	rr := do(srv.Router(), http.MethodPost, "/v1/send", body, key)
	if rr.Code != http.StatusOK {
		t.Fatalf("send = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	var resp sendResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 2 || resp.Results[0].Status != fcm.StatusDelivered {
		t.Fatalf("unexpected results: %+v", resp.Results)
	}
	if len(fake.got) != 2 {
		t.Errorf("fake FCM saw %d messages, want 2", len(fake.got))
	}
}

// A collapse id sent by a server has to reach FCM, otherwise a member in several groups
// still gets one notification per copy of a cross-posted check-in.
func TestSendForwardsCollapseID(t *testing.T) {
	srv, fake, store := newTestServer(t, defaultCfg())
	key, _ := store.Issue(context.Background(), "")

	body := `{"messages":[{"token":"a","title":"hi","collapseId":"xpost-42"}]}`
	rr := do(srv.Router(), http.MethodPost, "/v1/send", body, key)
	if rr.Code != http.StatusOK {
		t.Fatalf("send = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	if len(fake.got) != 1 {
		t.Fatalf("fake FCM saw %d messages, want 1", len(fake.got))
	}
	if fake.got[0].CollapseID != "xpost-42" {
		t.Errorf("collapse id = %q, want xpost-42", fake.got[0].CollapseID)
	}
}

// Bodies decode with DisallowUnknownFields, so the compat guarantee runs the other way too:
// a server that predates the field, or one sending an event that needs no collapsing, still
// gets a 200 and a message FCM sees exactly as before.
func TestSendAcceptsAMessageWithoutCollapseID(t *testing.T) {
	srv, fake, store := newTestServer(t, defaultCfg())
	key, _ := store.Issue(context.Background(), "")

	body := `{"messages":[{"token":"a","title":"hi","body":"there","data":{"type":"post"}}]}`
	rr := do(srv.Router(), http.MethodPost, "/v1/send", body, key)
	if rr.Code != http.StatusOK {
		t.Fatalf("old-shaped send = %d, want 200 (body %s)", rr.Code, rr.Body)
	}
	if len(fake.got) != 1 {
		t.Fatalf("fake FCM saw %d messages, want 1", len(fake.got))
	}
	if fake.got[0].CollapseID != "" {
		t.Errorf("collapse id = %q, want empty for a request that omits it", fake.got[0].CollapseID)
	}
}

func TestSendRejectsOversizedBatch(t *testing.T) {
	cfg := defaultCfg()
	cfg.MaxMessages = 2
	srv, _, store := newTestServer(t, cfg)
	key, _ := store.Issue(context.Background(), "")

	body := `{"messages":[{"token":"a"},{"token":"b"},{"token":"c"}]}`
	rr := do(srv.Router(), http.MethodPost, "/v1/send", body, key)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversized batch = %d, want 400", rr.Code)
	}
}

func TestSendRateLimitedPerKey(t *testing.T) {
	cfg := defaultCfg()
	cfg.SendBurst = 1
	cfg.SendPerMinute = 1
	srv, _, store := newTestServer(t, cfg)
	key, _ := store.Issue(context.Background(), "")
	h := srv.Router()

	body := `{"messages":[{"token":"a"}]}`
	if rr := do(h, http.MethodPost, "/v1/send", body, key); rr.Code != http.StatusOK {
		t.Fatalf("first send = %d, want 200", rr.Code)
	}
	if rr := do(h, http.MethodPost, "/v1/send", body, key); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second send = %d, want 429", rr.Code)
	}
}

func TestAdminRevokeStopsDelivery(t *testing.T) {
	cfg := defaultCfg()
	cfg.AdminToken = "admin-secret"
	srv, _, store := newTestServer(t, cfg)
	key, _ := store.Issue(context.Background(), "")
	k, _ := store.Verify(context.Background(), key)
	h := srv.Router()

	// A send works before revocation.
	if rr := do(h, http.MethodPost, "/v1/send", `{"messages":[{"token":"a"}]}`, key); rr.Code != http.StatusOK {
		t.Fatalf("pre-revoke send = %d, want 200", rr.Code)
	}
	// Admin needs the token.
	if rr := do(h, http.MethodGet, "/admin/keys", "", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("admin without token = %d, want 401", rr.Code)
	}
	// Revoke with the token.
	revokePath := "/admin/keys/" + strconv.FormatInt(k.ID, 10) + "/revoke"
	if rr := do(h, http.MethodPost, revokePath, "", "admin-secret"); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", rr.Code)
	}
	// The key no longer sends.
	if rr := do(h, http.MethodPost, "/v1/send", `{"messages":[{"token":"a"}]}`, key); rr.Code != http.StatusUnauthorized {
		t.Fatalf("post-revoke send = %d, want 401", rr.Code)
	}
}
