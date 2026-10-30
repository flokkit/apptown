package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"
)

func TestConnectionFailuresAreTypedAndPreserveTheirCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"network", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, true},
		{"parent deadline", context.DeadlineExceeded, false},
		{"wrapped parent deadline", fmt.Errorf("sweep: %w", context.DeadlineExceeded), false},
		{"dial deadline", &net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded}, true},
		{"cancelled", context.Canceled, false},
		{"redirect refused", &url.Error{Op: "Get", URL: "https://provider.test", Err: errors.New("redirect refused")}, false},
		{"untyped", errors.New("host unreachable"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ConnectionError(tc.err)
			if IsUnreachable(fmt.Errorf("provider: %w", got)) != tc.want || !errors.Is(got, tc.err) {
				t.Fatalf("classification: %v", got)
			}
		})
	}
	if ConnectionError(nil) != nil || Unreachable(nil) != nil {
		t.Fatal("success became a failure")
	}
}
