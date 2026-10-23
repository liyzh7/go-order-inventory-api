//go:build integration

package integration_test

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPFailureAssertionOmitsSecrets(t *testing.T) {
	r := httptest.NewRecorder()
	r.WriteHeader(201)
	secret := strings.Repeat("T", 43)
	r.Body.WriteString(`{"data":{"access_token":"` + secret + `","password":"synthetic-private-password"}}`)
	message := statusFailureMessage(r, 200)
	if !strings.Contains(message, "status=201 want=200") {
		t.Fatal("status diagnostics lost")
	}
	for _, s := range []string{secret, "synthetic-private-password", "access_token", "password"} {
		if strings.Contains(message, s) {
			t.Fatal("test assertion exposed response secrets")
		}
	}
}
