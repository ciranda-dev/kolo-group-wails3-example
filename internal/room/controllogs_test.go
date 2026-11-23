package room

import (
	"errors"
	"testing"

	kololog "github.com/ciranda-dev/kolo-log"
	kolostore "github.com/ciranda-dev/kolo-store"
)

// replicaHeader returns the header of a new log of the device of nd, kept in a store of its own.
func replicaHeader(t *testing.T, nd *tnode, r *Room) []byte {
	t.Helper()
	l, err := kolostore.New(kolostore.NewMemory()).Create(nd.dev, kololog.WithDerivedKeys(r.logSec, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.Header().Encode()
}

// ctlNext and ctlPrev are applied only from the control log of a device that is an admin, as ctlOpen and ctlDir are.
func TestNextAndPrevNeedAnAdmin(t *testing.T) {
	gr := newGroup(t, 3, Limits{})
	gr.waitMesh(2)
	m, other := gr.rooms[1], gr.nodes[2]
	e := m.Status().Epoch
	from := &roomLog{owner: [32]byte(other.key()), ctl: true, epoch: e, view: emptyView{}}
	next := replicaHeader(t, other, m)
	hd, err := kololog.DecodeLogHeader(next)
	if err != nil {
		t.Fatal(err)
	}
	added := m.applyCtl(from, 3, &ctlRecord{Kind: ctlNext, Epoch: e + 1, Header: next})
	m.logMu.Lock()
	followed := m.logs[keyOf(from.owner, hd.LogID)] != nil
	m.logMu.Unlock()
	if added || followed {
		t.Fatal("a member followed the next log of a device that is no admin")
	}
	from = &roomLog{owner: [32]byte(other.key()), ctl: true, epoch: e, view: emptyView{}}
	m.applyCtl(from, 0, &ctlRecord{Kind: ctlPrev, Epoch: e, Header: replicaHeader(t, other, m)})
	m.hmu.Lock()
	defer m.hmu.Unlock()
	for _, p := range m.prevs {
		if p.owner == from.owner {
			t.Fatal("a member kept the previous log of a device that is no admin")
		}
	}
}

type emptyView struct{}

func (emptyView) Len() uint64                { return 0 }
func (emptyView) Get(uint64) ([]byte, error) { return nil, errors.New("empty") }
