package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"strings"
)

type dummyBackend struct {
	Backend
}

func TestHostValidation(t *testing.T) {
	srv := New(Options{Host: "127.0.0.1", Port: 8420})

	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.1:8420", true},
		{"localhost", true},
		{"localhost:8420", true},
		{"::1", true},
		{"[::1]:8420", true},
		{"attacker.com", false},
		{"evil.com:8420", false},
		{"192.168.1.1:8420", false},
	}

	for _, tc := range cases {
		got := srv.isAllowedHost(tc.host)
		if got != tc.want {
			t.Errorf("isAllowedHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestCrossSiteProtection(t *testing.T) {
	srv := New(Options{Host: "127.0.0.1", Port: 8420})

	// Legitimate same-origin request
	r1 := httptest.NewRequest("POST", "/api/command", nil)
	r1.Header.Set("Sec-Fetch-Site", "same-origin")
	r1.Header.Set("Origin", "http://127.0.0.1:8420")
	if srv.isCrossSite(r1) {
		t.Error("isCrossSite reported true for legitimate same-origin request")
	}

	// Malicious cross-site request (Sec-Fetch-Site)
	r2 := httptest.NewRequest("POST", "/api/command", nil)
	r2.Header.Set("Sec-Fetch-Site", "cross-site")
	if !srv.isCrossSite(r2) {
		t.Error("isCrossSite failed to detect cross-site Sec-Fetch-Site")
	}

	// Malicious cross-site request (Origin)
	r3 := httptest.NewRequest("POST", "/api/command", nil)
	r3.Header.Set("Origin", "http://evil.com")
	if !srv.isCrossSite(r3) {
		t.Error("isCrossSite failed to detect evil.com Origin")
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv := New(Options{Host: "127.0.0.1", Port: 8420, Token: "testtoken123"})
	handler := srv.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Host = "127.0.0.1:8420"
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Error("Content-Security-Policy header is missing")
	}
}

func TestAutoTokenAuth(t *testing.T) {
	srv := New(Options{Host: "127.0.0.1", Port: 8420, Token: "supersecrettoken99"})
	mux := http.NewServeMux()
	srv.routes(mux)
	handler := srv.middleware(mux)

	// 1. Visit root "/" without any auth credentials
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "127.0.0.1:8420"
	req.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	// Verify cookie was set
	cookies := rec.Result().Cookies()
	var foundCookie bool
	for _, ck := range cookies {
		if ck.Name == "aurora_token" && ck.Value == "supersecrettoken99" {
			foundCookie = true
			break
		}
	}
	if !foundCookie {
		t.Errorf("expected aurora_token cookie to be set automatically")
	}

	// Verify token injection in HTML body
	body := rec.Body.String()
	if !strings.Contains(body, "supersecrettoken99") {
		t.Errorf("expected HTML body to contain injected token")
	}

	// 2. Local request to /api/token without token should be accepted via isLocalRequest
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/api/token", nil)
	req2.Host = "127.0.0.1:8420"
	req2.RemoteAddr = "127.0.0.1:54322"
	handler.ServeHTTP(rec2, req2)

	if rec2.Code == http.StatusUnauthorized {
		t.Errorf("local request to /api/token should not be unauthorized")
	}
}
