package server

import (
	"net/http"
	"testing"
)

func TestSubmitPublicNetworkRequestIsForwarded(t *testing.T) {
	c, fake, api, _ := signed(t)
	code, _, raw := c.do("POST", api+"/tasks", map[string]any{"message": "research", "network_request": map[string]any{"scope": "public", "minutes": 15, "reason": "research"}})
	if code != http.StatusCreated {
		t.Fatalf("%d %s", code, raw)
	}
	request := fake.submits[len(fake.submits)-1]["networkRequest"].(map[string]any)
	if request["scope"] != "NETWORK_SCOPE_PUBLIC" || request["minutes"] != float64(15) {
		t.Fatalf("%v", request)
	}
	before := len(fake.submits)
	code, _, _ = c.do("POST", api+"/tasks", map[string]any{"message": "research", "network_request": map[string]any{"scope": "public", "hosts": []string{"example.com"}, "minutes": 15}})
	if code != 400 || len(fake.submits) != before {
		t.Fatalf("invalid public request: %d", code)
	}
}
func TestLegacyNetworkRequestDefaultsToHosts(t *testing.T) {
	n := taskNetworkRequest{Hosts: []string{"example.com"}, Minutes: 15}
	if err := n.validate(); err != nil {
		t.Fatal(err)
	}
	if n.wire()["scope"] != "NETWORK_SCOPE_HOSTS" {
		t.Fatal(n.wire())
	}
}

func TestNetworkPolicyProjectionUsesSessionToken(t *testing.T) {
	c, fake, _, _ := signed(t)
	fake.networkPolicy = map[string]any{"allowed": true, "modes": []string{"deny", "open"}, "hosts": []string{}, "max_grant_minutes": 30, "secret": "not-for-browser"}
	code, body, raw := c.do("GET", "/api/network-policy?tenant=someone-else", nil)
	if code != 200 || body["allowed"] != true || body["secret"] != nil {
		t.Fatalf("%d %s", code, raw)
	}
	if len(fake.tokens) == 0 || fake.tokens[len(fake.tokens)-1] == "" {
		t.Fatal("missing forwarded auth")
	}
}

func TestUnlimitedNetworkRequestNeedsExplicitChoice(t *testing.T) {
	for _, req := range []taskNetworkRequest{{Scope: "public", Minutes: 0}, {Scope: "public", Minutes: 60, Unlimited: true}} {
		if req.validate() == nil {
			t.Fatal("implicit or contradictory unlimited choice accepted")
		}
	}
	req := taskNetworkRequest{Scope: "public", Minutes: 0, Unlimited: true}
	if err := req.validate(); err != nil {
		t.Fatal(err)
	}
	if req.wire()["unlimited"] != true || req.wire()["minutes"] != 0 {
		t.Fatal(req.wire())
	}
	c, fake, _, _ := signed(t)
	fake.networkPolicy = map[string]any{"allowed": true, "modes": []string{"deny", "allowlist", "open"}, "supports_unlimited": true, "max_grant_minutes": 60}
	status, body, _ := c.do("GET", "/api/network-policy", nil)
	if status != 200 || body["supports_unlimited"] != true {
		t.Fatal(status, body)
	}
}
