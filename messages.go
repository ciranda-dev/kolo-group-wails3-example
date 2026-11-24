package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"

	kolostore "github.com/ciranda-dev/kolo-store"
)

// msgStore keeps the messages the window was told of, since a room delivers a message once: after a restart the window
// reads them from here. A message is one record under m/<room>/<sequence number>, and i/<room>/<message id> names its number.
type msgStore struct {
	b    kolostore.Backend
	mu   sync.Mutex
	last map[string]uint64 // the highest sequence number of each room
}

func newMsgStore(b kolostore.Backend) *msgStore { return &msgStore{b: b, last: map[string]uint64{}} }

func msgKey(room string, seq uint64) []byte {
	return binary.BigEndian.AppendUint64([]byte("m/"+room+"/"), seq)
}

func idxKey(room, id string) []byte { return []byte("i/" + room + "/" + id) }

// add keeps a message that is not kept yet and returns it with its number; kept says whether it was new.
func (s *msgStore) add(room string, m MessageInfo) (out MessageInfo, kept bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, err := s.b.Get(idxKey(room, m.ID)); err == nil {
		out, err = s.get(room, binary.BigEndian.Uint64(v))
		return out, false, err
	} else if !errors.Is(err, kolostore.ErrNotFound) {
		return m, false, err
	}
	seq, ok := s.last[room]
	if !ok {
		if err := s.b.Scan([]byte("m/"+room+"/"), []byte("m/"+room+"0"), true, 1, func(k, _ []byte) bool {
			seq = binary.BigEndian.Uint64(k[len(k)-8:])
			return false
		}); err != nil {
			return m, false, err
		}
	}
	m.Seq = seq + 1 // numbers start at 1: 0 means the newest in a request
	s.last[room] = m.Seq
	rec, err := json.Marshal(m)
	if err != nil {
		return m, false, err
	}
	b := s.b.NewBatch()
	b.Put(msgKey(room, m.Seq), rec)
	b.Put(idxKey(room, m.ID), binary.BigEndian.AppendUint64(nil, m.Seq))
	return m, true, b.Commit()
}

func (s *msgStore) get(room string, seq uint64) (m MessageInfo, err error) {
	v, err := s.b.Get(msgKey(room, seq))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(v, &m)
}

// stored marks a message as held by a segment.
func (s *msgStore) stored(room, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.b.Get(idxKey(room, id))
	if errors.Is(err, kolostore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	m, err := s.get(room, binary.BigEndian.Uint64(v))
	if err != nil {
		return err
	}
	m.Stored = true
	rec, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.b.Put(msgKey(room, m.Seq), rec)
}

// page returns up to n messages numbered below before (0 for the newest), oldest first.
func (s *msgStore) page(room string, before uint64, n int) ([]MessageInfo, error) {
	end := []byte("m/" + room + "0")
	if before > 0 {
		end = msgKey(room, before)
	}
	out := []MessageInfo{}
	var derr error
	err := s.b.Scan([]byte("m/"+room+"/"), end, true, n, func(_, v []byte) bool {
		var m MessageInfo
		if derr = json.Unmarshal(v, &m); derr != nil {
			return false
		}
		out = append([]MessageInfo{m}, out...)
		return true
	})
	return out, errors.Join(err, derr)
}

// forget deletes everything kept for a room.
func (s *msgStore) forget(room string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.last, room)
	b := s.b.NewBatch()
	b.DeleteRange([]byte("m/"+room+"/"), []byte("m/"+room+"0"))
	b.DeleteRange([]byte("i/"+room+"/"), []byte("i/"+room+"0"))
	return b.Commit()
}
