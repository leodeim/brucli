package runner

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
	"github.com/leodeim/brucli/internal/vars"
)

func (s *Session) oauth2Auth(ctx context.Context, auth collection.Auth, sent *script.Request, scope *vars.Scope) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client, cleanup, err := s.clientForScope(scope)
	if err != nil {
		return err
	}
	defer cleanup()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 OAuth redirects")
		}
		if !sameOrigin(req.URL, via[0].URL) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	data, _ := json.Marshal(auth.Options)
	hash := sha256.Sum256(data)
	key := hex.EncodeToString(hash[:])
	token := s.tokens[key]
	if explicit := authValue(auth, "accessToken", "token"); explicit != "" {
		token = &oauth2.Token{AccessToken: explicit, TokenType: "Bearer"}
	}
	config := oauth2.Config{
		ClientID:     authValue(auth, "credentials.clientId", "clientId"),
		ClientSecret: authValue(auth, "credentials.clientSecret", "clientSecret"),
		Endpoint:     oauth2.Endpoint{TokenURL: authValue(auth, "accessTokenUrl"), AuthURL: authValue(auth, "authorizationUrl"), AuthStyle: oauth2.AuthStyleInParams},
		RedirectURL:  authValue(auth, "callbackUrl"), Scopes: strings.Fields(authValue(auth, "scope")),
	}
	if authValue(auth, "credentials.placement", "credentialsPlacement") == "basic_auth_header" {
		config.Endpoint.AuthStyle = oauth2.AuthStyleInHeader
	}
	if token != nil && !token.Valid() && token.RefreshToken != "" && authValue(auth, "settings.autoRefreshToken", "autoRefreshToken") != "false" {
		refresh := config
		refresh.Endpoint.TokenURL = cmp.Or(authValue(auth, "refreshTokenUrl"), config.Endpoint.TokenURL)
		token, err = refresh.TokenSource(ctx, token).Token()
		if err != nil {
			return fmt.Errorf("refresh OAuth 2 token: %w", err)
		}
	}
	if token == nil || !token.Valid() {
		if authValue(auth, "settings.autoFetchToken", "autoFetchToken") == "false" {
			return errors.New("OAuth 2 has no valid token and automatic token fetching is disabled")
		}
		flow := cmp.Or(authValue(auth, "flow", "grantType"), "client_credentials")
		switch flow {
		case "client_credentials":
			token, err = (&clientcredentials.Config{ClientID: config.ClientID, ClientSecret: config.ClientSecret, TokenURL: config.Endpoint.TokenURL, Scopes: config.Scopes, AuthStyle: config.Endpoint.AuthStyle}).Token(ctx)
		case "resource_owner_password_credentials", "password":
			token, err = config.PasswordCredentialsToken(ctx, authValue(auth, "resourceOwner.username", "username"), authValue(auth, "resourceOwner.password", "password"))
		case "authorization_code":
			if code := authValue(auth, "authorizationCode", "code"); code != "" {
				var options []oauth2.AuthCodeOption
				if verifier := authValue(auth, "codeVerifier"); verifier != "" {
					options = append(options, oauth2.VerifierOption(verifier))
				}
				token, err = config.Exchange(ctx, code, options...)
			} else {
				token, err = s.oauthBrowserLogin(ctx, config, auth, false)
			}
		case "implicit":
			token, err = s.oauthBrowserLogin(ctx, config, auth, true)
		default:
			return fmt.Errorf("unsupported OAuth 2 flow %q", flow)
		}
		if err != nil {
			return fmt.Errorf("fetch OAuth 2 token: %w", err)
		}
	}
	if token == nil || token.AccessToken == "" {
		return errors.New("OAuth 2 returned an empty access token")
	}
	if s.tokens == nil {
		s.tokens = map[string]*oauth2.Token{}
	}
	s.tokens[key] = token
	if queryKey, queryPlacement := oauth2QueryKey(auth); queryPlacement {
		u, err := url.Parse(sent.URL)
		if err != nil {
			return err
		}
		query := u.Query()
		query.Set(queryKey, token.AccessToken)
		u.RawQuery = query.Encode()
		sent.URL = u.String()
	} else {
		prefix := cmp.Or(authValue(auth, "tokenConfig.placement.header", "tokenHeaderPrefix"), "Bearer")
		sent.SetHeader("Authorization", prefix+" "+token.AccessToken)
	}
	return nil
}

func oauth2QueryKey(auth collection.Auth) (string, bool) {
	key := authValue(auth, "tokenConfig.placement.query", "tokenQueryKey")
	placement := authValue(auth, "tokenPlacement")
	return cmp.Or(key, "access_token"), key != "" || placement == "query" || placement == "queryparams" || placement == "url"
}

type oauthLoginResult struct{ code, accessToken, tokenType, error string }

func (s *Session) oauthBrowserLogin(ctx context.Context, config oauth2.Config, auth collection.Auth, implicit bool) (*oauth2.Token, error) {
	if !s.Interactive {
		return nil, errors.New("browser login requires the UI; supply an accessToken or authorizationCode for a headless run")
	}
	authorize, err := url.Parse(config.Endpoint.AuthURL)
	if err != nil || authorize.Host == "" || (authorize.Scheme != "https" && authorize.Scheme != "http") {
		return nil, errors.New("invalid OAuth authorization URL")
	}
	callback, err := url.Parse(cmp.Or(config.RedirectURL, "http://127.0.0.1:0/callback"))
	if err != nil || callback.Scheme != "http" {
		return nil, errors.New("OAuth 2 browser login requires an HTTP loopback callback URL")
	}
	host := callback.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("OAuth 2 callback must use localhost or a loopback IP address")
	}
	bindHost := host
	if host == "localhost" {
		bindHost = "127.0.0.1"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(bindHost, cmp.Or(callback.Port(), "80")))
	if err != nil {
		return nil, fmt.Errorf("listen for OAuth callback: %w", err)
	}
	defer listener.Close()
	if callback.Port() == "0" {
		_, port, _ := net.SplitHostPort(listener.Addr().String())
		callback.Host = net.JoinHostPort(host, port)
	}
	if callback.Path == "" {
		callback.Path = "/"
	}
	config.RedirectURL = callback.String()
	state := cmp.Or(authValue(auth, "state"), oauth2.GenerateVerifier())
	verifier := oauth2.GenerateVerifier()
	var options []oauth2.AuthCodeOption
	if authValue(auth, "pkce") == "true" || authValue(auth, "pkce.method") == "S256" {
		options = append(options, oauth2.S256ChallengeOption(verifier))
	}
	if implicit {
		options = append(options, oauth2.SetAuthURLParam("response_type", "token"))
	}
	results := make(chan oauthLoginResult, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path != callback.Path {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if implicit && query.Get("access_token") == "" && query.Get("error") == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<!doctype html><title>brucli sign-in</title><script>if(location.hash){location.replace(location.pathname+'?'+location.hash.slice(1));}</script>Completing sign-in…`)
			return
		}
		if query.Get("state") != state {
			http.Error(w, "Invalid OAuth state.", http.StatusBadRequest)
			return
		}
		result := oauthLoginResult{code: query.Get("code"), accessToken: query.Get("access_token"), tokenType: query.Get("token_type"), error: query.Get("error")}
		if result.code == "" && result.accessToken == "" && result.error == "" {
			http.Error(w, "Missing authorization result.", http.StatusBadRequest)
			return
		}
		select {
		case results <- result:
		default:
		}
		io.WriteString(w, "Signed in. You can close this tab.")
	})}
	go server.Serve(listener)
	defer server.Close()
	open := s.OpenBrowser
	if open == nil {
		open = openAuthBrowser
	}
	if err := open(config.AuthCodeURL(state, options...)); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-results:
		if result.error != "" {
			return nil, fmt.Errorf("authorization denied: %s", result.error)
		}
		if implicit {
			return &oauth2.Token{AccessToken: result.accessToken, TokenType: cmp.Or(result.tokenType, "Bearer")}, nil
		}
		if len(options) > 0 {
			return config.Exchange(ctx, result.code, oauth2.VerifierOption(verifier))
		}
		return config.Exchange(ctx, result.code)
	}
}

func openAuthBrowser(target string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	return exec.Command(command, target).Run()
}
