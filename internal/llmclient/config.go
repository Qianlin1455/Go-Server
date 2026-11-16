package llmclient

import (
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config is populated from the process environment. Its zero value is disabled.
type Config struct {
	endpoint string
	model    string
	apiKey   string
}

func (c Config) Enabled() bool { return c.endpoint != "" }

func (Config) String() string   { return "llmclient.Config" }
func (Config) GoString() string { return "llmclient.Config" }

func LoadConfig() (Config, error) {
	baseURL := strings.TrimSpace(os.Getenv("AI_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("AI_MODEL"))
	apiKey := strings.TrimSpace(os.Getenv("AI_API_KEY"))
	if baseURL == "" && model == "" && apiKey == "" {
		return Config{}, nil
	}
	if baseURL == "" || model == "" || apiKey == "" || strings.IndexFunc(apiKey, invalidBearerChar) >= 0 {
		return Config{}, ErrInvalidConfig
	}
	base, err := url.Parse(baseURL)
	if err != nil || !validBaseURL(base, baseURL) {
		return Config{}, ErrInvalidConfig
	}
	return Config{
		endpoint: strings.TrimRight(base.String(), "/") + "/chat/completions",
		model:    model,
		apiKey:   apiKey,
	}, nil
}

func invalidBearerChar(r rune) bool { return r <= ' ' || r >= 0x7f }

func validBaseURL(base *url.URL, raw string) bool {
	host := base.Hostname()
	if host == "" || base.Opaque != "" || base.User != nil || base.ForceQuery ||
		base.RawQuery != "" || strings.Contains(raw, "#") || strings.HasSuffix(base.Host, ":") {
		return false
	}
	ip := net.ParseIP(host)
	if strings.HasPrefix(base.Host, "[") && ip == nil {
		return false
	}
	if port := base.Port(); port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return false
		}
	}
	switch base.Scheme {
	case "https":
		return true
	case "http":
		return strings.EqualFold(host, "localhost") || ip.IsLoopback()
	default:
		return false
	}
}
