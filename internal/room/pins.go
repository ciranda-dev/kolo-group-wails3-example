package room

import (
	"context"
	"errors"
	"fmt"
	"slices"

	kolostore "github.com/ciranda-dev/kolo-store"

	"github.com/ciranda-dev/kolo-mls/settle"
)

// pins keeps what an admin's Commits need, the Commits, SettledCommits and trees, on a kolo-store backend.
type pins struct{ b kolostore.Backend }

func (p pins) Put(_ context.Context, key string, value []byte) error {
	return p.b.Put([]byte(key), value)
}

func (p pins) Get(_ context.Context, key string, max int) ([]byte, error) {
	v, err := p.b.Get([]byte(key))
	switch {
	case errors.Is(err, kolostore.ErrNotFound):
		return nil, fmt.Errorf("%w: %s", settle.ErrMissing, key)
	case err != nil:
		return nil, err
	case max > 0 && len(v) > max:
		return nil, settle.ErrPinTooLarge
	}
	return v, nil
}

func (p pins) List(_ context.Context, prefix string, max int) ([]string, error) {
	var out []string
	end := slices.Clone([]byte(prefix))
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			end = end[:i+1]
			break
		}
		end = end[:i]
	}
	if len(end) == 0 {
		end = nil
	}
	err := p.b.Scan([]byte(prefix), end, false, 0, func(k, _ []byte) bool {
		out = append(out, string(k))
		return max <= 0 || len(out) <= max
	})
	if err != nil {
		return nil, err
	}
	if max > 0 && len(out) > max {
		return nil, settle.ErrPinTooLarge
	}
	return out, nil
}
