package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nc1107/check-in-relay/internal/keys"
)

type registerReq struct {
	PublicURL string `json:"publicUrl"`
}

type registerResp struct {
	Key string `json:"key"`
}

// handleRegister mints a scoped key for a self-hosting server. It is self-serve: a server
// calls this once on first boot and stores the returned key. Registration is IP-rate-limited
// so the endpoint can't be used to mass-mint. A registrant that proves it points at a real,
// reachable Check-In server earns the higher send tier; the check is best-effort and its
// failure only lowers the tier, never blocks registration.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.registerLim.Allow(clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too many registrations from this address, try again later")
		return
	}
	var req registerReq
	// An empty body is fine: publicUrl is optional, so a bare POST just gets a basic key.
	if err := decodeJSON(w, r, &req, 4<<10); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	tier, label := keys.TierBasic, ""
	if u := strings.TrimSpace(req.PublicURL); u != "" && s.verifyCheckInServer(r.Context(), u) {
		tier, label = keys.TierVerified, u
	}
	key, err := s.keys.Issue(r.Context(), label, tier)
	if err != nil {
		log.Printf("relay: issue key: %v", err)
		writeErr(w, http.StatusInternalServerError, "could not issue key")
		return
	}
	log.Printf("relay: issued key tier=%s verified=%t", tier, tier == keys.TierVerified)
	writeJSON(w, http.StatusOK, registerResp{Key: key})
}

// verifyCheckInServer does a best-effort GET of the registrant's /api/server-info and
// checks it answers with the shape a real Check-In server returns. A reachable, correctly
// shaped server is a cheap bar that blocks casual mass-minting.
func (s *Server) verifyCheckInServer(ctx context.Context, publicURL string) bool {
	u, err := url.Parse(publicURL)
	// Require https: a real Check-In server is served over TLS, and it keeps this from
	// reaching plaintext internal endpoints.
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	endpoint := strings.TrimRight(publicURL, "/") + "/api/server-info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	resp, err := s.verifyClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var info struct {
		Name        *string `json:"name"`
		Initialized *bool   `json:"initialized"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&info); err != nil {
		return false
	}
	return info.Name != nil && info.Initialized != nil
}

// newVerifyClient builds the client used to reach a registrant's public URL. It refuses to
// connect to non-public IPs and does not follow redirects, so a supplied URL cannot be
// turned into a probe of the relay's internal network (SSRF).
func newVerifyClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if !isPublicIP(ip.IP) {
						return nil, fmt.Errorf("refusing to connect to non-public address")
					}
				}
				// Dial a vetted IP directly so DNS can't be re-resolved to a private
				// address between the check and the connection.
				return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
			},
		},
	}
}

func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	return true
}
