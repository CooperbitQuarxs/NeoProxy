package middleware

import "testing"

func TestNeoIdentityRoutesNeverCaptureCredentials(t *testing.T) {
	for _, path := range []string{"/v8/session", "/v8/session/bootstrap", "/v8/neo/users", "/v8/neo/users/u_member/password", "/v8/neo/users/u_member/keys"} {
		if shouldLogRequest(path) {
			t.Fatalf("credential-bearing route %s entered body/response capture", path)
		}
	}
	if !shouldLogRequest("/v1/chat/completions") {
		t.Fatal("ordinary inference logging changed")
	}
}
