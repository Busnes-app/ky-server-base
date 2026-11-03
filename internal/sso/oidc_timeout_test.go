package sso

import (
	"context"
	"net/http"
	"testing"

	"golang.org/x/oauth2"
)

// Discovery runs under the provider mutex, so an unbounded client lets one stalled IdP
// response hold every SSO login.
func TestOIDCHTTPClientIsAlwaysBounded(t *testing.T) {
	client, ok := strongOIDCContext(context.Background()).Value(oauth2.HTTPClient).(*http.Client)
	if !ok || client.Timeout == 0 {
		t.Fatalf("OIDC client has no timeout: %+v", client)
	}
}
