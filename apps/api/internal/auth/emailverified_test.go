package auth

import "testing"

// Under mode: proxy, Remote-Email comes from Authelia's own user directory,
// which an administrator controls, so it is trusted as verified. That trust is
// the whole reason the morning summary may be sent to it; it is documented in
// AGENTS.md next to the rest of proxy mode's trust model.
func TestProxyEmailIsTrustedAsVerified(t *testing.T) {
	a := mustNew(t, Config{Mode: ModeProxy})

	id := a.Identify(request("10.42.0.9:5555", map[string]string{
		HeaderUser: "wilant", HeaderEmail: "wilant@example.com", HeaderGroups: "cyclists",
	}))
	if id.Email != "wilant@example.com" || !id.EmailVerified {
		t.Errorf("identity = %+v, want a verified email", id)
	}

	id = a.Identify(request("10.42.0.9:5555", map[string]string{HeaderUser: "wilant", HeaderGroups: "cyclists"}))
	if id.Email != "" || id.EmailVerified {
		t.Errorf("identity = %+v: no Remote-Email, so nothing to call verified", id)
	}
}

// A header from a peer that is not the proxy is discarded entirely, verified
// flag included.
func TestUntrustedPeerCannotClaimAVerifiedEmail(t *testing.T) {
	a := mustNew(t, Config{Mode: ModeProxy, TrustedProxies: []string{"10.42.0.0/16"}})
	id := a.Identify(request("203.0.113.9:1234", map[string]string{HeaderUser: "x", HeaderEmail: "x@example.com"}))
	if id.EmailVerified || id.Email != "" {
		t.Errorf("identity = %+v", id)
	}
}

func TestLocalIdentityHasNoEmail(t *testing.T) {
	id := LocalIdentity()
	if id.Email != "" || id.EmailVerified {
		t.Errorf("mode none has no email to send to: %+v", id)
	}
}
