package api

import (
	"context"
	"net/http"
	"time"
)

// llmRequestBudget caps a model call made while a rider waits on the
// response. The client allows 120 s (a cold model load is slow), but the app
// is served through Cloudflare, which answers 524 after 100 s on its own. A
// shorter budget means the rider gets the app's own "could not reach the
// model" message instead of Cloudflare's error page. The background morning
// pass has no proxy in front of it and keeps the client's full budget.
var llmRequestBudget = 90 * time.Second

// llmRequestContext is r's context with llmRequestBudget applied.
func llmRequestContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), llmRequestBudget)
}
