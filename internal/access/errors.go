package access

import "errors"

// ErrAuthorityUnavailable: no authority was supplied to the check.
var ErrAuthorityUnavailable = errors.New("access authority is not configured")
