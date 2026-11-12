package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/notification"
	"github.com/flokkit/apptown/internal/fault"
)

type notificationDenial struct{ capability access.Capability }

func (a *notificationDenial) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	a.capability = p.Capability
	return access.Caller{}, fault.New(fault.Forbidden, "Not available to you")
}
func TestNotificationTestRequiresConnectorPermission(t *testing.T) {
	t.Parallel()
	auth := &notificationDenial{}
	h := NewNotifications(notification.New(notification.Dependencies{Authority: auth}))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/channels/phone/test", nil)
	r.SetPathValue("name", "phone")
	w := httptest.NewRecorder()
	h.Test(w, r)
	if w.Code != http.StatusForbidden || auth.capability != access.CapIntegrationConnector {
		t.Fatalf("test authority: %d %s", w.Code, auth.capability)
	}
}
