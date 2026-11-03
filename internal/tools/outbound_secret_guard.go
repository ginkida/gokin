package tools

import (
	"net/url"

	"gokin/internal/security"
)

// outboundSecretRefusal is returned when web_fetch/web_search would send what
// looks like a credential to a third party. The executor's redactor cleans
// tool OUTPUT only; arguments leave the machine verbatim, so a model that read
// a token (or was prompt-injected into exfiltrating one) could otherwise ship
// it in a URL or search query with no prompt — both tools are auto-allowed.
const outboundSecretRefusal = "request refused: the argument contains what looks like a credential (a known token format, a key=value secret, or a value gokin knows is secret). Remove it; secrets must never be sent to external services."

// outboundArgumentCarriesSecret checks the argument as written and URL-decoded
// (a percent-encoded token is still the token).
func outboundArgumentCarriesSecret(value string) bool {
	if value == "" {
		return false
	}
	if security.ContainsHighConfidenceSecret(value) {
		return true
	}
	if decoded, err := url.QueryUnescape(value); err == nil && decoded != value {
		return security.ContainsHighConfidenceSecret(decoded)
	}
	return false
}
