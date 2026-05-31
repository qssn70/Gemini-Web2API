package gemini

import (
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	fakeUA "github.com/lib4u/fake-useragent"

	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

type ProfileConfig struct {
	Profile    profiles.ClientProfile
	Browser    string
	OS         []string
	FallbackUA string
}

var profileConfigs = []ProfileConfig{
	{profiles.Chrome_133, "Chrome", []string{"Windows", "Mac OS X", "Linux"}, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"},
	{profiles.Chrome_131, "Chrome", []string{"Windows", "Mac OS X", "Linux"}, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"},
	{profiles.Chrome_124, "Chrome", []string{"Windows", "Mac OS X"}, "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"},
	{profiles.Chrome_120, "Chrome", []string{"Windows", "Mac OS X"}, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
}

var (
	currentProfile   ProfileConfig
	currentUserAgent string
	profileMu        sync.RWMutex
	rng              *rand.Rand
	uaGenerator      *fakeUA.UserAgent
)

func init() {
	rng = rand.New(rand.NewSource(time.Now().UnixNano()))

	var err error
	uaGenerator, err = fakeUA.New()
	if err != nil {
		log.Printf("Warning: Failed to init fake-useragent, using fallbacks: %v", err)
	}

	selectRandomProfile()
}

func selectRandomProfile() {
	idx := rng.Intn(len(profileConfigs))
	currentProfile = profileConfigs[idx]
	currentUserAgent = generateUserAgentForProfile(currentProfile)
}

func generateUserAgentForProfile(config ProfileConfig) string {
	// Always use the hardcoded fallback UA that matches the TLS profile.
	// The fake-useragent library can generate mismatched UAs (e.g. Safari
	// UA with Chrome TLS profile) which triggers Google's bot detection —
	// the server cross-references the TLS fingerprint (JA3/JA4) against
	// the User-Agent header and flags mismatches, returning metadata-only
	// frames instead of candidate content.
	return config.FallbackUA
}

func GetRandomProfile() ProfileConfig {
	profileMu.Lock()
	defer profileMu.Unlock()
	selectRandomProfile()
	return currentProfile
}

func GetCurrentProfile() ProfileConfig {
	profileMu.RLock()
	defer profileMu.RUnlock()
	return currentProfile
}

func GetCurrentUserAgent() string {
	profileMu.RLock()
	defer profileMu.RUnlock()
	return currentUserAgent
}

// IPFamily values control which network family (IPv4 / IPv6) the
// underlying tls-client dialer is allowed to use. The zero value behaves
// like the operating system default (Happy Eyeballs / IPv6-preferred).
//
// We expose this because Google's Gemini infrastructure occasionally
// flags one family from a given host (most commonly the host's IPv6
// /64) while the other is fine — letting users pin a family lets them
// route around regional / IP-block issues without changing the proxy.
const (
	IPFamilyAuto = "auto"
	IPFamilyIPv4 = "ipv4"
	IPFamilyIPv6 = "ipv6"
)

// NormalizeIPFamily canonicalises whatever the user wrote in .env (e.g.
// "v4", "IPv4", "4", "ipv4") into one of the IPFamily* constants. Empty
// input and unknown values fall back to IPFamilyAuto. Returned bool
// tells the caller whether the input was recognised — useful for log
// warnings about typos like "ipv44".
func NormalizeIPFamily(raw string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "", "auto", "any", "default", "system":
		return IPFamilyAuto, v == "" || v == "auto" || v == "any" || v == "default" || v == "system"
	case "4", "v4", "ipv4", "tcp4":
		return IPFamilyIPv4, true
	case "6", "v6", "ipv6", "tcp6":
		return IPFamilyIPv6, true
	default:
		return IPFamilyAuto, false
	}
}

func GetClientOptions(profile ProfileConfig, proxyURL string, ipFamily string) []tls_client.HttpClientOption {
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(600),
		tls_client.WithClientProfile(profile.Profile),
		tls_client.WithNotFollowRedirects(),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
		tls_client.WithRandomTLSExtensionOrder(),
	}

	switch ipFamily {
	case IPFamilyIPv4:
		// Disabling IPv6 forces every dial to "tcp4". This is the
		// fix for "my host's IPv6 prefix is geo-flagged but IPv4
		// works" on Gemini.
		options = append(options, tls_client.WithDisableIPV6())
	case IPFamilyIPv6:
		options = append(options, tls_client.WithDisableIPV4())
	}

	if strings.TrimSpace(proxyURL) != "" {
		options = append(options, tls_client.WithProxyUrl(strings.TrimSpace(proxyURL)))
	}

	return options
}

func RandomDelay() {
	delay := time.Duration(100+rng.Intn(200)) * time.Millisecond
	time.Sleep(delay)
}
