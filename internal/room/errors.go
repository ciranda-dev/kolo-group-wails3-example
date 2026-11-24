package room

import (
	"errors"
	"fmt"
)

// The errors of the room; each starts with the package prefix and works with errors.Is.
var (
	ErrNotImplemented = errors.New("room: not implemented")
	ErrInvalid        = errors.New("room: invalid argument")
	ErrClosed         = errors.New("room: closed")
	ErrRemoved        = errors.New("room: removed from the room")
	ErrNotAdmin       = errors.New("room: not an admin device")
	ErrNotMember      = errors.New("room: not a member device")
	ErrUnknownRoom    = errors.New("room: unknown room")
	ErrMalformed      = errors.New("room: malformed message")
	ErrWrongEpoch     = errors.New("room: message of another epoch")
	ErrOpen           = errors.New("room: message does not open")
	ErrRate           = errors.New("room: sender over its rate")
	ErrTooLarge       = errors.New("room: message too large")
	ErrTooManyParents = errors.New("room: too many parents")
	ErrBadTime        = errors.New("room: message time out of range")
)

func errInvalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}
