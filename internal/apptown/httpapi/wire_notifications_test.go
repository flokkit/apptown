package httpapi

import (
	"github.com/flokkit/apptown/internal/apptown/notification"
	"github.com/flokkit/apptown/internal/wiregen"
)

func init() {
	registerWireMapped(notification.Channel{}, "NotificationChannel")
	registerWireMapped(notification.Delivery{}, "NotificationDelivery")
	registerWire(wiregen.Named{Name: "NotificationView", Value: notification.View{}}, wiregen.Named{Name: "NotificationChannel", Value: notification.Channel{}}, wiregen.Named{Name: "NotificationDelivery", Value: notification.Delivery{}})
}
