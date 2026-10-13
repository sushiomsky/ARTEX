package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The return value of GetSetting for "key does not exist" and "read error" is only one error: in both cases, the value is
// Empty string. Once the password-related handler treats the error as "the password has not been set", it will
// Open the initialization entrance - authInit will allow an unauthenticated request to overwrite the existing administrator password.
// authStatus will send the front end directly to /setup to do this.
//
// These two tests turn off the handler's connection pool to cause read failures, asserting that both are fail closed.
func TestAuthStatusFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	// Turn off the pool and let subsequent GetSetting return error instead of sql.ErrNoRows.
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	w := httptest.NewRecorder()
	s.authStatus(w, httptest.NewRequest("GET", "/api/auth/status", nil))

	if w.Code != 503 {
		t.Fatalf("status=%d want 503 (read failure is treated as uninitialized and the user will be sent to /setup to overwrite the password); body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Initialized *bool `json:"initialized"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err == nil && payload.Initialized != nil {
		t.Fatalf("Should not answer initialized when read failed, got %v", *payload.Initialized)
	}
}

func TestAuthInitFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"password":"correct horse battery"}`)
	s.authInit(w, httptest.NewRequest("POST", "/api/auth/init", body))

	if w.Code != 503 {
		t.Fatalf("status=%d want 503 (allowing unauthenticated requests to overwrite existing passwords when reading fails); body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "token") {
		t.Fatalf("Token should not be issued on read failure: %s", w.Body.String())
	}
}

func TestValidatePassword(t *testing.T) {
	for _, tc := range []struct {
		name, pw string
		wantErr  bool
	}{
		{"empty", "", true},
		{"seven", "1234567", true},
		{"eight bits", "12345678", false},
		{"Eight Chinese characters are counted by the number of characters rather than the number of bytes", "密码密码密码密码", false},
		{"Three Chinese characters are enough for 9 bytes but only 3 characters", "密码强", true},
		{"72 bytes", strings.Repeat("a", 72), false},
		{"73 bytes exceeds bcrypt limit", strings.Repeat("a", 73), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validatePassword(tc.pw); (got != "") != tc.wantErr {
				t.Fatalf("validatePassword(%q)=%q, wantErr=%v", tc.pw, got, tc.wantErr)
			}
		})
	}
}
