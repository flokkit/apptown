//go:build !linux

package supervisor

import "errors"

func processUID(int) (uint32, error) {
	return 0, errors.New("managed process observation requires Linux")
}
func managedChild(int, Registration) (Observation, error) {
	return Observation{}, errors.New("managed process observation requires Linux")
}
