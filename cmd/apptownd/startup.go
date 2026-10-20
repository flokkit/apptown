package main

import "fmt"

// Keep the required database repair ahead of every operation that can resume
// execution or serve writes. Optional/provider recovery belongs inside start.
func recoverThenStart(recover func() error, start func() error) error {
	if err := recover(); err != nil {
		return fmt.Errorf("required startup recovery failed: %w", err)
	}
	return start()
}
