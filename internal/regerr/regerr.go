// Package regerr classifies registry operation failures for the SDK retry
// contract (module.Retryable / module.Permanent), shared by the registry
// components.
package regerr

import (
	"errors"
	"net"
	"net/http"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/tiny-systems/module/module"
)

// Classify wraps err with retryability derived from the registry response:
// 429 and 5xx are transient (a backoff retry can clear them), 401/403/404 are
// permanent (auth and not-found cannot be retried into success), and plain
// network failures that never produced a response are transient. Anything
// else is returned unmarked and defaults to no-retry.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	var terr *transport.Error
	if errors.As(err, &terr) {
		switch {
		case terr.StatusCode == http.StatusTooManyRequests || terr.StatusCode >= 500:
			return module.Retryable(err)
		case terr.StatusCode == http.StatusUnauthorized ||
			terr.StatusCode == http.StatusForbidden ||
			terr.StatusCode == http.StatusNotFound:
			return module.Permanent(err)
		case terr.Temporary():
			// Registry-signalled transient error codes on other statuses.
			return module.Retryable(err)
		}
		return err
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return module.Retryable(err)
	}
	return err
}
