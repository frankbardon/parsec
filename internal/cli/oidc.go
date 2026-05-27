package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/frankbardon/parsec/descriptor"
	ucli "github.com/urfave/cli/v3"
)

// LoginCommand groups OIDC-based login flows. Today the only
// supported provider is generic OIDC via the device-authorization
// grant — the IdP must implement RFC 8628.
//
// The flow:
//   1. Fetch the issuer's discovery document to find the
//      device-authorization endpoint and token endpoint.
//   2. POST to device-authorization with the configured client_id;
//      receive a user_code, verification_uri, device_code, interval.
//   3. Print user_code + verification_uri so the operator can complete
//      the flow in a browser.
//   4. Poll the token endpoint at `interval` until the operator
//      approves (status: success) or the device_code expires.
//   5. Persist the resulting ID token to ~/.parsec/credentials (0600)
//      so subsequent `parsec --token` invocations pick it up via the
//      Token() helper.
func LoginCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "login",
		Usage: "Authenticate against an OIDC identity provider",
		Commands: []*ucli.Command{
			loginOIDCCommand(),
		},
	}
}

// LogoutCommand removes the persisted credentials file.
func LogoutCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "logout",
		Usage: "Discard any persisted OIDC credentials",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			path, err := credentialsPath()
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.Writer, "no credentials to remove")
					return nil
				}
				return err
			}
			fmt.Fprintf(cmd.Writer, "removed %s\n", path)
			return nil
		},
	}
}

func loginOIDCCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "oidc",
		Usage: "Run the OIDC device-code flow against the configured issuer",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "issuer", Required: true, Usage: "OIDC issuer URL (e.g. https://accounts.google.com)", Sources: ucli.EnvVars("PARSEC_OIDC_ISSUER")},
			&ucli.StringFlag{Name: "client-id", Required: true, Usage: "OAuth client identifier registered with the IdP", Sources: ucli.EnvVars("PARSEC_OIDC_CLIENT_ID")},
			&ucli.StringFlag{Name: "client-secret", Usage: "OAuth client secret (omit for public clients)", Sources: ucli.EnvVars("PARSEC_OIDC_CLIENT_SECRET")},
			&ucli.StringSliceFlag{Name: "scope", Value: []string{"openid", "email", "profile", "groups"}, Usage: "OAuth scopes to request"},
			&ucli.DurationFlag{Name: "timeout", Value: 5 * time.Minute, Usage: "Total time to wait for the operator to approve the device code"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			ctx, cancel := context.WithTimeout(ctx, cmd.Duration("timeout"))
			defer cancel()

			provider, err := oidc.NewProvider(ctx, cmd.String("issuer"))
			if err != nil {
				return fmt.Errorf("OIDC discovery: %w", err)
			}
			endpoints, err := discoverDeviceEndpoints(ctx, cmd.String("issuer"))
			if err != nil {
				return err
			}
			scope := strings.Join(cmd.StringSlice("scope"), " ")
			dev, err := requestDeviceCode(ctx, endpoints.DeviceAuth, cmd.String("client-id"), scope)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.Writer, "Open %s in a browser and enter the code: %s\n", dev.VerificationURI, dev.UserCode)
			if dev.VerificationURIComplete != "" {
				fmt.Fprintf(cmd.Writer, "Or visit %s directly.\n", dev.VerificationURIComplete)
			}
			tok, err := pollDeviceToken(ctx, provider, endpoints.Token, dev, cmd.String("client-id"), cmd.String("client-secret"))
			if err != nil {
				return err
			}
			if tok.IDToken == "" {
				return fmt.Errorf("issuer returned no id_token; ensure the openid scope is requested")
			}
			path, err := writeCredentials(tok)
			if err != nil {
				return err
			}
			res := struct {
				Path      string    `json:"path"`
				Subject   string    `json:"subject,omitempty"`
				ExpiresAt time.Time `json:"expires_at,omitempty"`
			}{Path: path, Subject: tok.Subject, ExpiresAt: tok.ExpiresAt}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.login.oidc", res))
		},
	}
}

// deviceEndpoints captures the discovery fields we need for the
// device-authorization flow. go-oidc's Provider does not expose
// device_authorization_endpoint directly, so we fetch the document
// once and extract both URLs.
type deviceEndpoints struct {
	DeviceAuth string
	Token      string
}

func discoverDeviceEndpoints(ctx context.Context, issuer string) (deviceEndpoints, error) {
	wellKnown := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return deviceEndpoints{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return deviceEndpoints{}, fmt.Errorf("fetch %s: %w", wellKnown, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return deviceEndpoints{}, fmt.Errorf("fetch %s: status %d", wellKnown, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return deviceEndpoints{}, err
	}
	var doc struct {
		DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
		TokenEndpoint               string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return deviceEndpoints{}, fmt.Errorf("parse discovery doc: %w", err)
	}
	if doc.DeviceAuthorizationEndpoint == "" {
		return deviceEndpoints{}, fmt.Errorf("issuer %s does not advertise device_authorization_endpoint", issuer)
	}
	if doc.TokenEndpoint == "" {
		return deviceEndpoints{}, fmt.Errorf("issuer %s does not advertise token_endpoint", issuer)
	}
	return deviceEndpoints{DeviceAuth: doc.DeviceAuthorizationEndpoint, Token: doc.TokenEndpoint}, nil
}

// deviceCodeResponse matches the RFC 8628 device-authorization
// response shape.
type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func requestDeviceCode(ctx context.Context, endpoint, clientID, scope string) (deviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return deviceCodeResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return deviceCodeResponse{}, fmt.Errorf("device authorization: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return deviceCodeResponse{}, fmt.Errorf("device authorization: status %d: %s", resp.StatusCode, body)
	}
	var dev deviceCodeResponse
	if err := json.Unmarshal(body, &dev); err != nil {
		return deviceCodeResponse{}, fmt.Errorf("device authorization: parse: %w", err)
	}
	if dev.Interval <= 0 {
		dev.Interval = 5 // RFC 8628 default
	}
	return dev, nil
}

// loginResult holds the persisted credential. Sub + Expiry are
// extracted from the ID token when possible so the CLI can echo
// useful information without re-decoding the token.
type loginResult struct {
	IDToken   string    `json:"id_token"`
	Subject   string    `json:"subject,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// pollDeviceToken polls the token endpoint at the IdP-advertised
// interval until the operator approves the device code, the code
// expires, or ctx is canceled.
func pollDeviceToken(ctx context.Context, provider *oidc.Provider, tokenEndpoint string, dev deviceCodeResponse, clientID, clientSecret string) (loginResult, error) {
	interval := time.Duration(dev.Interval) * time.Second
	verifier := provider.Verifier(&oidc.Config{ClientID: clientID})
	for {
		select {
		case <-ctx.Done():
			return loginResult{}, ctx.Err()
		case <-time.After(interval):
		}
		form := url.Values{}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("device_code", dev.DeviceCode)
		form.Set("client_id", clientID)
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return loginResult{}, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return loginResult{}, fmt.Errorf("token poll: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var tok struct {
				IDToken     string `json:"id_token"`
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(body, &tok); err != nil {
				return loginResult{}, fmt.Errorf("token response: %w", err)
			}
			res := loginResult{IDToken: tok.IDToken}
			// Best-effort claim extraction. If verification fails we
			// still persist the token — the server will re-verify on
			// every request.
			if id, err := verifier.Verify(ctx, tok.IDToken); err == nil {
				res.Subject = id.Subject
				res.ExpiresAt = id.Expiry
			}
			return res, nil
		}
		var oerr struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &oerr)
		switch oerr.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "expired_token":
			return loginResult{}, fmt.Errorf("device code expired before approval")
		case "access_denied":
			return loginResult{}, fmt.Errorf("operator denied the login request")
		default:
			return loginResult{}, fmt.Errorf("token poll: %s: %s", oerr.Error, oerr.ErrorDescription)
		}
	}
}

// credentialsPath returns ~/.parsec/credentials. The directory is
// created on demand with mode 0700.
func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".parsec")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return filepath.Join(dir, "credentials"), nil
}

// writeCredentials persists the ID token to disk with mode 0600. The
// token is the literal bytes operators paste into Authorization:
// Bearer headers — keeping the file exactly that lets us short-circuit
// any future encoding decisions.
func writeCredentials(res loginResult) (string, error) {
	path, err := credentialsPath()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(res.IDToken), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// ReadCredentials returns the persisted OIDC ID token, or "" when no
// credentials file exists. Exposed for the CLI's --token resolution
// helper.
func ReadCredentials() string {
	path, err := credentialsPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
