// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

import (
	"context"
	"time"
)

// SetWait replaces how the gateway waits before it sends a call once more
// after a server error, so that a test can act during the wait.
func SetWait(g *Gateway, wait func(ctx context.Context, d time.Duration) error) {
	g.wait = wait
}
