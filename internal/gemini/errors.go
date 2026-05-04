package gemini

import (
	"errors"
	"fmt"
	"strings"
)

// Bard error codes returned via the BardErrorInfo type URL on the Gemini
// streaming endpoint. These are sourced from upstream
// HanaokaYuzu/Gemini-API src/gemini_webapi/constants.py (`ErrorCode` enum).
//
// They appear at part[5][2][0][1][0] in the framing protocol response, e.g.
//
//	[["wrb.fr",null,null,null,null,[9,null,
//	  [["type.googleapis.com/assistant.boq.bard.application.BardErrorInfo",
//	    [1060]]]]]]
const (
	BardErrTemporary1013     = 1013 // transient server error, retryable
	BardErrUsageLimit        = 1037 // model quota / daily limit reached
	BardErrModelInconsistent = 1050 // model parameter changed mid-conversation
	BardErrModelHeaderBad    = 1052 // model header outdated; library needs update
	BardErrIPBlocked         = 1060 // IP/region temporarily blocked or unsupported
)

// BardError is returned by the response parser when the upstream service
// embedded a BardErrorInfo envelope instead of a candidate set. It is a
// recoverable, structured error: callers can use errors.As to inspect Code
// and decide whether to retry on another account, surface a 429, etc.
type BardError struct {
	Code    int
	Message string
}

func (e *BardError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return fmt.Sprintf("BARD_ERROR_%d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("BARD_ERROR_%d", e.Code)
}

// IsBardError unwraps err and reports whether it is a BardError. The
// extracted *BardError pointer is returned for callers that want the code.
func IsBardError(err error) (*BardError, bool) {
	if err == nil {
		return nil, false
	}
	var be *BardError
	if errors.As(err, &be) {
		return be, true
	}
	return nil, false
}

// NewBardError constructs a BardError with a friendly, operator-actionable
// message for codes we recognise. Unknown codes get a generic but
// still-useful description.
func NewBardError(code int) *BardError {
	switch code {
	case BardErrTemporary1013:
		return &BardError{Code: code, Message: "Gemini transient error 1013 (retryable). Try again in a moment; if it persists, the upstream service is having issues."}
	case BardErrUsageLimit:
		return &BardError{Code: code, Message: "Gemini usage / rate limit hit. Wait a few minutes, switch to a different model (e.g. gemini-2.5-flash), or check account limits at gemini.google.com."}
	case BardErrModelInconsistent:
		return &BardError{Code: code, Message: "The model parameter is inconsistent with the existing conversation. Use the same model for an entire session, or start a new chat."}
	case BardErrModelHeaderBad:
		return &BardError{Code: code, Message: "Model header rejected by upstream — the model identifier in the request is outdated or the model is not available. Try a different model from /v1/models."}
	case BardErrIPBlocked:
		return &BardError{
			Code: code,
			Message: "Gemini blocked the request: the server's IP is temporarily flagged, or your region/account is not eligible for this model. " +
				"Common fixes: (1) use a stable, residential-grade HTTP/SOCKS5 proxy via PROXY=… ; (2) avoid datacenter IPs that Google flags as bots; " +
				"(3) try again in 10–60 minutes — temporary blocks typically auto-clear; (4) confirm your Google account region supports this model.",
		}
	default:
		return &BardError{Code: code, Message: fmt.Sprintf("Unknown Gemini API error code %d. This may be a transient Google-side issue or a new error not yet handled.", code)}
	}
}

// IsRetryableBardError reports whether code is worth retrying — possibly on
// a different account in the load balancer pool. 1013 is the canonical
// transient error; 1060 (IP block) is *partially* retryable: a different
// account that has its own proxy may succeed, so we treat it as retryable
// at the pool level even though sleeping won't help.
func IsRetryableBardError(code int) bool {
	switch code {
	case BardErrTemporary1013, BardErrIPBlocked:
		return true
	default:
		return false
	}
}

// IsAccountLevelBardError reports whether code indicates the *account* (not
// the request) is the problem, so the balancer should cooldown this entry
// and prefer a different one. 1037 (usage limit) and 1060 (IP block) both
// qualify — the next request from the same account/IP will hit the same
// wall.
func IsAccountLevelBardError(code int) bool {
	switch code {
	case BardErrUsageLimit, BardErrIPBlocked:
		return true
	default:
		return false
	}
}

// FormatBardError pretty-prints err for log messages. Returns "" if err is
// not a BardError.
func FormatBardError(err error) string {
	if be, ok := IsBardError(err); ok {
		return strings.TrimSpace(be.Error())
	}
	return ""
}
