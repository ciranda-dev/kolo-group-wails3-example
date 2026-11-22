package room

import (
	"context"

	kolobraid "github.com/ciranda-dev/kolo-braid"
	"github.com/ciranda-dev/kolo-braid/devices"
	koloid "github.com/ciranda-dev/kolo-id"
	kolostore "github.com/ciranda-dev/kolo-store"

	"github.com/ciranda-dev/kolo-mls/braid"
)

const braidApp = "kolo-group/v0"

// braidParts are what the admins' braid of a room needs besides its options: the index of the Commits and the roster.
func braidParts(backend kolostore.Backend, prefix []byte, lim *kolobraid.Limits) (kolobraid.Options, *braid.Index, *devices.Store, error) {
	roster, err := devices.New([]byte("devices/"), nil)
	if err != nil {
		return kolobraid.Options{}, nil, nil, err
	}
	idx := braid.NewIndex([]byte("mls/"), roster)
	apply := func(tx *kolobraid.Tx, e *kolobraid.Entry) error {
		if ok, err := idx.Apply(tx, e); ok || err != nil {
			return err
		}
		_, err := roster.Apply(tx, e)
		return err
	}
	return kolobraid.Options{Backend: backend, Prefix: prefix, App: braidApp, Apply: apply, Limits: lim}, idx, roster, nil
}

// newBraid creates the admins' braid of a room, with this device as its first admin and settler.
func newBraid(ctx context.Context, id *koloid.Identity, backend kolostore.Backend, prefix []byte, lim *kolobraid.Limits) (*kolobraid.Braid, *braid.BraidOrder, error) {
	opts, idx, roster, err := braidParts(backend, prefix, lim)
	if err != nil {
		return nil, nil, err
	}
	b, err := kolobraid.Create(ctx, id, kolobraid.GroupConfig{App: braidApp}, opts)
	if err != nil {
		return nil, nil, err
	}
	return b, braid.NewBraidOrder(b, idx, roster), nil
}

// openBraid opens the braid a restart finds in the backend.
func openBraid(ctx context.Context, id *koloid.Identity, backend kolostore.Backend, prefix []byte, lim *kolobraid.Limits) (*kolobraid.Braid, *braid.BraidOrder, error) {
	opts, idx, roster, err := braidParts(backend, prefix, lim)
	if err != nil {
		return nil, nil, err
	}
	b, err := kolobraid.Open(ctx, id, opts)
	if err != nil {
		return nil, nil, err
	}
	return b, braid.NewBraidOrder(b, idx, roster), nil
}
