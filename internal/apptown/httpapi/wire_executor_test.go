package httpapi

import "github.com/flokkit/apptown/internal/wiregen"

func init() {
	registerWire(wiregen.Named{Name: "ExecutorView", Value: ExecutorView{}}, wiregen.Named{Name: "ExecutingStepsView", Value: ExecutingStepsView{}})
}
