package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// A page session lets a page inside an app, such as the Connect tab of
// PrusaSlicer, act with the app's pairing credential without holding it
// (ADR-20261112-nay). The page sends the credential once, as a bearer token,
// and gets back an HttpOnly cookie that no script on the page reads. Revoking
// the app's credential ends the page session at once.

// pageSessionPath is where a page trades the app's credential for the cookie.
const pageSessionPath = "/api/v1/auth/app-pairings/session"

// pageSessionsPerCredential is how many page sessions one app pairing
// credential may open from one address in the span the pairing requests are
// counted in. The slicer opens one when it shows a page and one more before
// the last ends. The count is kept per credential, so a caller with any
// other token cannot spend it and lock a slicer out.
const pageSessionsPerCredential = 30

// The errors an AppPairings implementation reports from OpenPageSession.
var (
	// ErrNotAppCredential is a credential that is not a live app pairing
	// credential.
	ErrNotAppCredential = errors.New("httpapi: only an app pairing credential opens a page session")
	// ErrPageSessionLimited is a credential that opened too many page
	// sessions from this address.
	ErrPageSessionLimited = errors.New("httpapi: the credential opened too many page sessions")
)

var (
	pageSessionRefused = fault.Register(fault.DomainAuth(), "PAGE_SESSION_REFUSED", http.StatusForbidden,
		fault.Document(
			"Only an app pairing credential opens a page session",
			"Send the credential an app collected through the app pairing as Authorization: Bearer. "+
				"A session, a credential a person minted and a page session open none, and a cookie is never read here.",
			fault.RetryNever,
			fault.ShowDetail,
		))

	pageSessionLimited = fault.Register(fault.DomainAuth(), "PAGE_SESSION_LIMITED", http.StatusTooManyRequests,
		fault.Document(
			"This app opened too many page sessions from this address",
			"Wait ten minutes, then open the page again.",
			fault.RetryAllowed,
			fault.ShowDetail,
		))
)

// PageSession is a page session the implementation opened.
type PageSession struct {
	Token     string
	ExpiresAt time.Time
}

// pageSessionCookieBody is the cookie the answer sets, spelled out for an app
// that puts it into its web view's cookie store itself and does not parse
// Set-Cookie. Only the caller holding the app's credential reads it.
type pageSessionCookieBody struct {
	Name     string    `json:"name"      doc:"The cookie's name, the session cookie of this server."`
	Value    string    `json:"value"     doc:"The page session. Store it only in the cookie, never where a page script reads it."`
	Path     string    `json:"path"      doc:"The cookie's path."`
	Expires  time.Time `json:"expires"   doc:"When the cookie and the page session end."`
	HTTPOnly bool      `json:"http_only" doc:"Always true: no script on the page may read the cookie."`
	SameSite string    `json:"same_site" doc:"Always Strict."                                                                     enum:"Strict"`
	Secure   bool      `json:"secure"    doc:"True unless this server is reached over plain HTTP."`
}

type openPageSessionOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie" doc:"The page session, HttpOnly and SameSite=Strict, and Secure unless this server is reached over plain HTTP."`

	Body struct {
		ExpiresAt time.Time             `json:"expires_at" doc:"When the page session ends. It is at most an hour away, and never after the app's credential expires."`
		Cookie    pageSessionCookieBody `json:"cookie"     doc:"The cookie Set-Cookie sets, for an app that stores it into its web view itself."`
	}
}

func registerOpenPageSession(api huma.API, cfg Config, limiter *addressLimiter) {
	register(api, huma.Operation{
		OperationID: "open-page-session",
		Method:      http.MethodPost,
		Path:        pageSessionPath,
		Summary:     "Trade an app's credential for a page session cookie",
		Description: "An app, such as PrusaSlicer, sends the credential it collected through the app pairing " +
			"as Authorization: Bearer, bare or in its JWT shape, before it shows a page of this server. " +
			"The answer sets the session cookie to a page session: an api credential that speaks for the " +
			"same person with the same scopes, lives at most an hour, and ends when the app's credential " +
			"is revoked or expires. The cookie is HttpOnly, so no script on the page reads it, and " +
			"SameSite=Strict. The body carries the same cookie, so an app can put it into its web view " +
			"itself. The page never holds the credential. Only the header is read: a request that " +
			"carries the credential in a cookie is refused, so another site cannot open a page session. " +
			"One credential keeps at most four live page sessions, and opens thirty from one address in " +
			"ten minutes.",
		Tags:   []string{"auth"},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests},
	}, func(ctx context.Context, _ *struct{}) (*openPageSessionOutput, error) {
		request := requestFrom(ctx)

		token := bearerOf(request)
		if token == "" {
			return nil, pageSessionRefused.New("the credential was not sent as Authorization: Bearer")
		}

		address := clientAddress(cfg, request)
		admit := func(parent string) bool { return limiter.take(parent+"\x00"+address, time.Now()) }

		opened, err := cfg.AppPairings.OpenPageSession(ctx, token, admit)

		switch {
		case errors.Is(err, ErrNotAppCredential):
			return nil, pageSessionRefused.New("the credential is not an app pairing credential")
		case errors.Is(err, ErrPageSessionLimited):
			return nil, pageSessionLimited.New("this credential has opened too many page sessions from this address")
		case err != nil:
			return nil, serverFault(err, "open the page session")
		}

		cookie := pageSessionCookie(cfg, opened)
		out := &openPageSessionOutput{SetCookie: cookie}
		out.Body.ExpiresAt = opened.ExpiresAt
		out.Body.Cookie = pageSessionCookieBody{
			Name: cookie.Name, Value: cookie.Value, Path: cookie.Path, Expires: cookie.Expires,
			HTTPOnly: cookie.HttpOnly, SameSite: "Strict", Secure: cookie.Secure,
		}

		return out, nil
	})
}

// bearerOf is the token in the Authorization header, and empty when the
// header names no bearer token. A cookie is never read, so a form or a link
// on another site cannot open a page session with the browser's cookies.
func bearerOf(r *http.Request) string {
	if r == nil {
		return ""
	}

	const prefix = "bearer "

	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}

	return strings.TrimSpace(header[len(prefix):])
}

// pageSessionCookie is the page session as a browser holds it. It takes the
// name of the session cookie, so every route that reads a session reads it,
// and SameSite=Strict, so no request another site starts carries it.
func pageSessionCookie(cfg Config, opened PageSession) http.Cookie {
	//nolint:gosec // G124: Secure follows the deployment's own scheme, as sessionCookie explains.
	return http.Cookie{
		Name:     cfg.Sessions.Cookie(),
		Value:    opened.Token,
		Path:     cookiePath,
		Expires:  opened.ExpiresAt,
		HttpOnly: true,
		Secure:   !cfg.InsecureCookies,
		SameSite: http.SameSiteStrictMode,
	}
}
