package daemon

import "github.com/kxn/codex-remote-feishu/internal/core/agentproto"

type workMemoryRecordKey struct{ Instance, Command, Thread string }
type workMemoryDeferredRecord struct {
	Instance string
	Event    agentproto.Event
}

// A single finite drain handles exact protocol correlations that arrive while
// snapshot I/O is busy. Failed writes remain quarantined by the durable pending
// record; there is no timer, retry loop, or lifecycle-event replay.
func (r *workMemoryRuntime) deferThreadRecord(instance string, event agentproto.Event) {
	r.proofMu.Lock()
	if r.deferredRecords == nil {
		r.deferredRecords = map[workMemoryRecordKey]workMemoryDeferredRecord{}
	}
	if len(r.deferredRecords) >= 64 {
		r.proofMu.Unlock()
		return
	}
	key := workMemoryRecordKey{Instance: instance, Command: event.CommandID, Thread: event.ThreadID}
	r.deferredRecords[key] = workMemoryDeferredRecord{Instance: instance, Event: event}
	if r.drainingRecords {
		r.proofMu.Unlock()
		return
	}
	r.drainingRecords = true
	r.proofMu.Unlock()
	go r.drainThreadRecords()
}
func (r *workMemoryRuntime) drainThreadRecords() {
	for {
		r.proofMu.Lock()
		var next workMemoryDeferredRecord
		for key, entry := range r.deferredRecords {
			next = entry
			delete(r.deferredRecords, key)
			break
		}
		if next.Instance == "" {
			r.drainingRecords = false
			r.proofMu.Unlock()
			return
		}
		r.proofMu.Unlock()
		r.busy <- struct{}{}
		r.recordThread(next.Instance, next.Event)
		<-r.busy
	}
}
