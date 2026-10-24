package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/ca"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"
)

func TestNormalizeRegion(t *testing.T) {
	cases := map[string]string{
		"US":    "US",
		"jp":    "JP",
		"CHN":   "CN",
		"usa":   "US",
		"HKG":   "HK",
		"ZZZ":   "",
		"C":     "",
		"CNAAA": "",
		"":      "",
		"1A":    "",
	}
	for input, want := range cases {
		if got := normalizeRegion(input); got != want {
			t.Errorf("normalizeRegion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeRegionCoversTheBlockLists(t *testing.T) {
	for _, code := range geminiBlockedRegions {
		if normalizeRegion(code) == "" {
			t.Errorf("gemini block list entry %q does not fold to an alpha-2 code", code)
		}
	}
	for _, code := range claudeBlockedRegions {
		if normalizeRegion(code) != code {
			t.Errorf("claude block list entry %q is not already alpha-2", code)
		}
	}
}

// The iOS endpoint turns API clients away even where ChatGPT works, so that
// refusal is the success signal and anything else is not.
func TestChatGptIosStatus(t *testing.T) {
	cases := map[string]string{
		"Request is not allowed. Please try again later.": serviceAvailable,
		"You may be connected to a disallowed ISP":        serviceDisallowedIsp,
		"Sorry, you have been blocked":                    serviceBlocked,
		"<html>something else entirely</html>":            serviceFailed,
		"":                                                serviceFailed,
	}
	for body, want := range cases {
		if got := chatGptIosStatus(&ProbeResult{Body: body}); got != want {
			t.Errorf("chatGptIosStatus(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestTikTokStatus(t *testing.T) {
	cases := []struct {
		code int
		body string
		want string
	}{
		{http.StatusOK, `{"region":"JP"}`, serviceAvailable},
		{http.StatusForbidden, "", serviceUnsupportedRegion},
		{http.StatusUnavailableForLegalReasons, "", serviceUnsupportedRegion},
		{http.StatusOK, "Access Denied", serviceUnsupportedRegion},
		{http.StatusOK, "TikTok is not available in your region", serviceUnsupportedRegion},
		{http.StatusInternalServerError, "", serviceFailed},
	}
	for _, testCase := range cases {
		got := tikTokStatus(&ProbeResult{StatusCode: testCase.code, Body: testCase.body})
		if got != testCase.want {
			t.Errorf("tikTokStatus(%d, %q) = %q, want %q", testCase.code, testCase.body, got, testCase.want)
		}
	}
}

func TestTikTokRegionReadsTheAppContext(t *testing.T) {
	const cluster = `{"idc":"no1a","vdc":"no1a","region":"EU-TTP2","vregion":"EU-TTP2"}`
	page := cluster + `{"__DEFAULT_SCOPE__":{"webapp.app-context":{"language":"en","region":"GB","appId":1233}}}`
	if got := tikTokRegion(page); got != "GB" {
		t.Errorf("region = %q, want the visitor's country rather than the serving cluster", got)
	}
	if got := tikTokRegion(cluster); got != "" {
		t.Errorf("region = %q, want empty without the app context", got)
	}
	if got := tikTokRegion(`{"webapp.app-context":{"region":"zh-Hant-TW"}}`); got != "" {
		t.Errorf("region = %q, want a locale left unread rather than its language", got)
	}
}

func TestBilibiliStatusReadsTheLeadingCode(t *testing.T) {
	cases := map[string]string{
		`{"code":0,"message":"success","result":{"code":0,"accept_format":"hdf`: serviceAvailable,
		`{"code":-10403,"message":"region locked"}`:                             serviceUnsupportedRegion,
		`{"code":-412,"message":"request blocked"}`:                             serviceFailed,
		`{"message":"success","result":{"code":0}}`:                             serviceFailed,
		"": serviceFailed,
	}
	for body, want := range cases {
		if got := bilibiliStatus(body); got != want {
			t.Errorf("bilibiliStatus(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestSpotifyRegionPrefersTheRedirectedPath(t *testing.T) {
	redirected := &ProbeResult{Url: "https://www.spotify.com/de-de/", Body: `{"countryCode":"US"}`}
	if got := spotifyRegion(redirected); got != "de" {
		t.Errorf("region = %q, want the path's country", got)
	}
	unredirected := &ProbeResult{
		Url:  "https://www.spotify.com/api/content/v1/country-selector",
		Body: `{"countryCode":"SE"}`,
	}
	if got := spotifyRegion(unredirected); got != "SE" {
		t.Errorf("region = %q, want the payload's country", got)
	}
}

func TestYouTubeRegionPatterns(t *testing.T) {
	bodies := map[string]string{
		`<span id="country-code"> GB </span>`: "GB",
		`{"GL":"JP","other":1}`:               "JP",
		`{"countryCode":"KR"}`:                "KR",
		`{"country_code":"BR"}`:               "BR",
	}
	for body, want := range bodies {
		var got string
		for _, pattern := range youTubeRegionPatterns {
			if code := firstSubmatch(pattern, body); code != "" {
				got = normalizeRegion(code)
				break
			}
		}
		if got != want {
			t.Errorf("region from %q = %q, want %q", body, got, want)
		}
	}
}

type serviceEndpointProxy struct {
	constant.Proxy
	address string
}

func (p *serviceEndpointProxy) DialContext(ctx context.Context, metadata *constant.Metadata) (constant.Conn, error) {
	local := *metadata
	if err := local.SetRemoteAddress(p.address); err != nil {
		return nil, err
	}
	return p.Proxy.DialContext(ctx, &local)
}

func TestYouTubeOrdinaryProbe(t *testing.T) {
	defer ca.ResetCertificate()
	for _, test := range []struct {
		name, body, status, region string
		code                       int
	}{
		{name: "ordinary-page", code: http.StatusOK, body: `<script>ytcfg.set({"GL":"DE"});</script>`, status: serviceAvailable, region: "DE"},
		{name: "premium-denied-only", code: http.StatusOK, body: `<script>ytcfg.set({"GL":"DE"});</script><span>YouTube Premium is not available in your country</span>`, status: serviceAvailable, region: "DE"},
		{name: "empty-success", code: http.StatusOK, status: serviceFailed},
		{name: "captcha", code: http.StatusOK, body: `<html>Please solve this captcha</html>`, status: serviceFailed},
		{name: "forbidden", code: http.StatusForbidden, body: `<html>Access denied</html>`, status: serviceRestricted},
		{name: "ordinary-region-denied", code: http.StatusOK, body: `<script>ytcfg.set({"GL":"RU"});</script><span>YouTube is not available in your country</span>`, status: serviceUnsupportedRegion, region: "RU"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Host != "www.youtube.com" || r.URL.Path != "/" || r.URL.Query().Get("hl") != "en" {
					t.Errorf("unexpected service endpoint %s%s", r.Host, r.URL.String())
				}
				w.WriteHeader(test.code)
				_, _ = w.Write([]byte(test.body))
			}))
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			template := &x509.Certificate{
				SerialNumber: big.NewInt(1), DNSNames: []string{"www.youtube.com"},
				NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
				KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
				IsCA:        true, BasicConstraintsValid: true,
			}
			certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
			if err != nil {
				t.Fatal(err)
			}
			if err = ca.AddCertificate(string(certificate)); err != nil {
				t.Fatal(err)
			}
			server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{certificate}, PrivateKey: key}}}
			server.StartTLS()
			defer server.Close()
			proxy := &serviceEndpointProxy{Proxy: namedProxy("youtube-probe"), address: server.Listener.Addr().String()}
			tunnel.UpdateProxies(map[string]constant.Proxy{"youtube-probe": proxy}, nil)
			defer tunnel.UpdateProxies(nil, nil)
			item := checkYouTube(serviceEnv{ctx: context.Background(), proxyName: "youtube-probe", timeout: time.Second})
			if item.Status != test.status || item.Region != test.region {
				t.Errorf("result %+v, want status=%s region=%s", item, test.status, test.region)
			}
			if requests != 1 || !slices.Equal(item.Chains, []string{"youtube-probe"}) {
				t.Errorf("requests=%d chains=%v, wanted one pinned ordinary-page request", requests, item.Chains)
			}
		})
	}
}

func TestDisneyAndPrimePatterns(t *testing.T) {
	if got := firstSubmatch(disneyAssertionPattern, `{"assertion":"abc.def"}`); got != "abc.def" {
		t.Errorf("assertion = %q", got)
	}
	if got := firstSubmatch(disneyRefreshPattern, `{"refresh_token":"tok123"}`); got != "tok123" {
		t.Errorf("refresh token = %q", got)
	}
	if got := firstSubmatch(disneySupportedPattern, `{"inSupportedLocation":false}`); got != "false" {
		t.Errorf("supported = %q", got)
	}
	if got := firstSubmatch(primeRegionPattern, `{"currentTerritory":"NL"}`); got != "NL" {
		t.Errorf("territory = %q", got)
	}
}
