package garmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Profile is the little Connect will say about whose account this is.
type Profile struct {
	DisplayName string `json:"displayName"`
	FullName    string `json:"fullName"`
}

// Name is what to show a rider: their own name if Connect gives one, and the
// opaque profile id only as a fallback.
func (p Profile) Name() string {
	if p.FullName != "" {
		return p.FullName
	}
	return p.DisplayName
}

// Profile fetches the signed-in account's own profile.
//
// Two jobs. It names the account in the UI, so a rider can see *which* Garmin
// they connected. More usefully it is the first call that actually exercises
// the OAuth2 exchange: signing in only proves the SSO ticket converted to an
// OAuth1 token, and a token that cannot be exchanged for a bearer would
// otherwise look like a successful connection until the first push.
//
// Undocumented like the rest, so callers treat a failure as "no name known"
// rather than "sign-in failed" — see api.LiveGarmin.
func (c *Client) Profile(ctx context.Context) (Profile, error) {
	bearer, err := c.bearerToken(ctx)
	if err != nil {
		return Profile{}, err
	}

	raw, status, err := c.do(ctx, http.MethodGet, c.APIBase+"/userprofile-service/socialProfile", nil, "",
		header{"Authorization", "Bearer " + bearer},
		header{"Accept", "application/json"},
		header{"X-Requested-With", "XMLHttpRequest"},
	)
	if err != nil {
		return Profile{}, err
	}
	if status != http.StatusOK {
		return Profile{}, fmt.Errorf("garmin: the profile request returned %d: %s", status, snippet(raw))
	}

	var profile Profile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return Profile{}, fmt.Errorf("garmin: unreadable profile response: %w", err)
	}
	if profile.DisplayName == "" && profile.FullName == "" {
		return Profile{}, errors.New("garmin: the profile response named nobody")
	}
	return profile, nil
}

// ProfileID returns the handle the wellness endpoints take in their path,
// asking Connect's profile once when the stored session predates
// Session.ProfileID, and keeping the answer on this client.
func (c *Client) ProfileID(ctx context.Context) (string, error) {
	if c.session.ProfileID != "" {
		return c.session.ProfileID, nil
	}
	p, err := c.Profile(ctx)
	if err != nil {
		return "", err
	}
	if p.DisplayName == "" {
		return "", errors.New("garmin: the profile response carried no displayName to ask the wellness endpoints for")
	}
	c.session.ProfileID = p.DisplayName
	return p.DisplayName, nil
}
