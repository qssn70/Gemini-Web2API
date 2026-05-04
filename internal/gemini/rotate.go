package gemini

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	http "github.com/bogdanfinn/fhttp"
)

// EndpointRotate is Google's RotateCookies endpoint that can be used to mint
// a fresh __Secure-1PSIDTS cookie when the existing one has expired but
// __Secure-1PSID is still valid.
//
// See: HanaokaYuzu/Gemini-API src/gemini_webapi/utils/rotate_1psidts.py
const EndpointRotate = "https://accounts.google.com/RotateCookies"

// RotateBody is the (intentionally opaque) payload Google's RotateCookies
// endpoint expects. The literal value comes from the upstream Python client.
const RotateBody = `[000,"-0000000000000000000"]`

// ErrRotateAuth is returned when RotateCookies responds with HTTP 401, which
// indicates that __Secure-1PSID itself is expired or rejected — only manual
// re-extraction from the browser can recover from this state.
type ErrRotateAuth struct {
	AccountID string
}

func (e *ErrRotateAuth) Error() string {
	id := e.AccountID
	if strings.TrimSpace(id) == "" {
		id = "default"
	}
	return fmt.Sprintf("AUTH_FAILED: account '%s' rotate cookies returned 401, __Secure-1PSID may be expired", id)
}

// RotateCookies refreshes the __Secure-1PSIDTS cookie by POSTing to Google's
// RotateCookies endpoint. On success, the new value is propagated to:
//
//   - c.Cookies (the in-memory map used by NewClient and other callers)
//   - the underlying tls-client cookie jar for both google.com and
//     gemini.google.com
//
// Returns the new __Secure-1PSIDTS value. A non-nil ErrRotateAuth means
// __Secure-1PSID itself is no longer valid; other errors are transient
// (network/HTTP) and worth retrying.
func (c *Client) RotateCookies() (string, error) {
	req, err := http.NewRequest(http.MethodPost, EndpointRotate, strings.NewReader(RotateBody))
	if err != nil {
		return "", fmt.Errorf("build rotate request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://accounts.google.com")
	req.Header.Set("Referer", "https://accounts.google.com/")
	req.Header.Set("User-Agent", GetCurrentUserAgent())
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", getLangHeader())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("rotate cookies request failed: %w", err)
	}
	defer resp.Body.Close()
	// Drain body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return "", &ErrRotateAuth{AccountID: c.AccountID}
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("rotate cookies returned status %d", resp.StatusCode)
	}

	// Look for the new __Secure-1PSIDTS in two places:
	//   1) the Set-Cookie headers of this response, and
	//   2) the cookie jar after the client processed Set-Cookie automatically.
	newPSIDTS := ""
	for _, ck := range resp.Cookies() {
		if ck.Name == "__Secure-1PSIDTS" && ck.Value != "" {
			newPSIDTS = ck.Value
			break
		}
	}
	if newPSIDTS == "" {
		// tls-client merges Set-Cookie into the jar for the request URL
		// (accounts.google.com). Pull the most recent value from there.
		if u, parseErr := url.Parse(EndpointRotate); parseErr == nil {
			for _, ck := range c.httpClient.GetCookies(u) {
				if ck.Name == "__Secure-1PSIDTS" && ck.Value != "" {
					newPSIDTS = ck.Value
					break
				}
			}
		}
	}

	if newPSIDTS == "" {
		return "", fmt.Errorf("rotate cookies succeeded but __Secure-1PSIDTS not present in response")
	}

	// Update in-memory cookie map and propagate to gemini.google.com host so
	// the next request (e.g. Init) picks up the fresh value immediately.
	if c.Cookies == nil {
		c.Cookies = make(map[string]string)
	}
	c.Cookies["__Secure-1PSIDTS"] = newPSIDTS
	c.syncCookieJar()

	return newPSIDTS, nil
}

// syncCookieJar mirrors c.Cookies into the tls-client cookie jar for the
// gemini.google.com host. NewClient does the same on construction; we re-do
// it here whenever cookies change at runtime.
func (c *Client) syncCookieJar() {
	u, err := url.Parse("https://gemini.google.com")
	if err != nil {
		return
	}
	cookieList := make([]*http.Cookie, 0, len(c.Cookies))
	for k, v := range c.Cookies {
		cookieList = append(cookieList, &http.Cookie{
			Name:   k,
			Value:  v,
			Domain: ".google.com",
			Path:   "/",
		})
	}
	c.httpClient.SetCookies(u, cookieList)
}

// shortCookieSuffix returns the last 6 characters of a cookie value, used for
// log messages that need to identify a token without dumping the full secret.
func shortCookieSuffix(v string) string {
	if len(v) <= 6 {
		return v
	}
	return v[len(v)-6:]
}
