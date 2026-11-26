package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// setupOIDCPath tries the sign-in provider the wizard would configure.
const setupOIDCPath = firstRunPath + "/oidc"

// SetupOIDCProbe is the sign-in provider the wizard would write.
type SetupOIDCProbe struct {
	Issuer       string `json:"issuer"                  doc:"The issuer address, as core.auth.issuer."                                 format:"uri" maxLength:"2048" minLength:"1"`
	ClientID     string `json:"client_id"               doc:"The client id, as core.auth.client_id."                                                maxLength:"512"  minLength:"1"`
	ClientSecret string `json:"client_secret,omitempty" doc:"The client secret, as core.auth.client_secret. Empty is a public client."              maxLength:"2048"`
	RedirectURI  string `json:"redirect_uri"            doc:"The redirect address, as core.auth.redirect_uri."                         format:"uri" maxLength:"2048" minLength:"1"`
}

// SetupOIDCStep is one part of the check.
type SetupOIDCStep struct {
	Status string `json:"status"           doc:"passed: the provider answered the way a working sign-in needs. failed: it refused. unknown: its answer reads neither way. skipped: an earlier step failed." enum:"passed,failed,unknown,skipped"`
	Detail string `json:"detail,omitempty" doc:"What the provider said, or why the step could not run."`
}

// SetupOIDCCheck is what trying the provider found.
type SetupOIDCCheck struct {
	Issuer    string        `json:"issuer,omitempty" doc:"The issuer the provider's discovery document names, when it was read."`
	Discovery SetupOIDCStep `json:"discovery"        doc:"Whether the discovery document was read and offers what a sign-in needs: the same issuer, the endpoints and S256 PKCE."`
	Client    SetupOIDCStep `json:"client"           doc:"Whether the token endpoint accepts the client id and secret. It is sent a code nobody issued, so a provider that took the client refuses the code instead."`
	Redirect  SetupOIDCStep `json:"redirect"         doc:"Whether the authorization endpoint takes the client id and redirect address. A pass is likely but not proof: a provider may refuse on a page it answers 200 with."`
}

type setupOIDCInput struct {
	Code string `header:"X-Setup-Code" maxLength:"128" doc:"The one-time setup code this server printed in its log when it started, or the value of core.auth.setup_code."`
	Body SetupOIDCProbe
}

type setupOIDCOutput struct {
	Body SetupOIDCCheck
}

func registerSetupOIDC(api huma.API, cfg Config) {
	register(api, huma.Operation{
		OperationID: "check-setup-oidc",
		Method:      http.MethodPost,
		Path:        setupOIDCPath,
		Summary:     "Try the sign-in provider the first run would configure",
		Description: "Tries an OpenID Connect provider before the wizard is confirmed, without signing anybody " +
			"in. It reads the discovery document, sends the token endpoint an authorization code nobody " +
			"issued to learn whether the client id and secret are taken, and opens the authorization " +
			"endpoint without following its redirect to learn whether the redirect address is. Every step " +
			"answers 200 with its verdict; a refusal is a failed step, not an error. It takes the one-time " +
			"setup code in `X-Setup-Code` and answers 409 once the first run is done.",
		Tags:     []string{"auth"},
		Security: noSecurity,
		Errors: []int{
			http.StatusForbidden, http.StatusConflict, http.StatusTooManyRequests,
			http.StatusInternalServerError,
		},
	}, func(ctx context.Context, in *setupOIDCInput) (*setupOIDCOutput, error) {
		check, err := cfg.Setup.CheckOIDC(ctx, setupClientOf(cfg, requestFrom(ctx)), in.Code, in.Body)
		if err != nil {
			return nil, withRetryAfter(serverFault(err, "try the sign-in provider"))
		}

		return &setupOIDCOutput{Body: check}, nil
	})
}
