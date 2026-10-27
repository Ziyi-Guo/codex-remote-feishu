package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

type workMemoryOwner struct {
	Surface      string   `json:"surface"`
	User         string   `json:"user"`
	Instance     string   `json:"instance"`
	KnownThreads []string `json:"known_threads,omitempty"`
}
type workMemoryOwners struct {
	SchemaVersion int                        `json:"schema_version"`
	Threads       map[string]workMemoryOwner `json:"threads"`
	Pending       map[string]workMemoryOwner `json:"pending"`
}

func readWorkMemoryOwners(root *os.Root) (workMemoryOwners, error) {
	owners := workMemoryOwners{SchemaVersion: 1, Threads: map[string]workMemoryOwner{}, Pending: map[string]workMemoryOwner{}}
	err := workMemoryReadJSON(root, "private-threads.json", 1<<20, &owners)
	if os.IsNotExist(err) {
		active, markerErr := workMemoryWasActivated(root.Name())
		if markerErr != nil {
			return owners, markerErr
		}
		if active {
			return owners, fmt.Errorf("activated ownership registry is missing")
		}
		return owners, nil
	}
	if err != nil {
		return owners, err
	}
	if owners.SchemaVersion != 1 || owners.Threads == nil || owners.Pending == nil {
		return owners, fmt.Errorf("invalid ownership")
	}
	for _, records := range []map[string]workMemoryOwner{owners.Threads, owners.Pending} {
		for key, owner := range records {
			if key == "" || owner.Surface == "" || owner.User == "" || owner.Instance == "" {
				return owners, fmt.Errorf("invalid ownership")
			}
		}
	}
	return owners, nil
}
func (o workMemoryOwners) guard(req workMemoryRequest) error {
	for _, thread := range []string{req.Command.Target.ThreadID, req.Command.Target.SourceThreadID} {
		if thread == "" {
			continue
		}
		if owner, ok := o.Threads[thread]; ok && !owner.allows(req) {
			return fmt.Errorf("此会话包含个人事项资料，仅原所有者的原会话入口可继续；请使用 /new。")
		}
		if req.Ordinary {
			continue
		}
		for _, owner := range o.Pending {
			if owner.allows(req) {
				continue
			}
			known := false
			for _, id := range owner.KnownThreads {
				if id == thread {
					known = true
					break
				}
			}
			if !known {
				return fmt.Errorf("此会话的个人事项归属尚未确认；请使用 /new 或已有普通会话。")
			}
		}
	}
	return nil
}
func (o workMemoryOwner) allows(req workMemoryRequest) bool {
	return o.Surface == req.Surface && o.User == req.Actor && o.User == req.QueueActor && req.Authorized
}
func (o *workMemoryOwners) claim(req workMemoryRequest, lifecycle string) error {
	owner := workMemoryOwner{Surface: req.Surface, User: req.Actor, Instance: req.Instance}
	if req.Command.Target.EffectivePromptExecutionMode() == agentproto.PromptExecutionModeResumeExisting && req.Command.Target.ThreadID != "" {
		o.Threads[req.Command.Target.ThreadID] = owner
	} else {
		if req.Command.CommandID == "" {
			return fmt.Errorf("missing command ID")
		}
		owner.KnownThreads = req.KnownThreads
		o.Pending[lifecycle+":"+req.Command.CommandID] = owner
	}
	return nil
}
func writeWorkMemoryOwners(root string, owners workMemoryOwners) error {
	if err := initializeWorkMemoryPrivacy(root); err != nil {
		return err
	}
	return writeWorkMemoryOwnersAtomic(root, owners)
}
func writeWorkMemoryOwnersAtomic(root string, owners workMemoryOwners) error {
	data, err := json.Marshal(owners)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("ownership limit")
	}
	f, err := os.CreateTemp(root, ".private-threads-*")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(path, filepath.Join(root, "private-threads.json"))
}

// Lifecycle events always advance core. Only explicit protocol command/thread
// correlation may resolve private ownership; unknown threads stay quarantined.
func (a *App) noteWorkMemoryEventLocked(instance string, event agentproto.Event) bool {
	if (event.Kind != agentproto.EventThreadDiscovered && event.Kind != agentproto.EventTurnStarted) || event.CommandID == "" || event.ThreadID == "" || event.Initiator.Kind != agentproto.InitiatorRemoteSurface {
		return true
	}
	if a.workMemory == nil && a.headlessRuntime.Paths.StateDir != "" {
		a.workMemory = &workMemoryRuntime{root: filepath.Join(a.headlessRuntime.Paths.StateDir, "work-memory"), lifecycle: a.daemonLifecycleID, busy: make(chan struct{}, 1)}
	}
	if a.workMemory == nil {
		return true
	}
	runtime := a.workMemory
	if runtime.noteOrdinaryThread(instance, event) {
		return true
	}
	a.mu.Unlock()
	result := make(chan bool, 1)
	select {
	case runtime.busy <- struct{}{}:
		go func() { value := runtime.recordThread(instance, event); <-runtime.busy; result <- value }()
	default:
		runtime.deferThreadRecord(instance, event)
		a.mu.Lock()
		return true
	}
	select {
	case <-result:
	case <-time.After(runtime.ioBudget()):
	}
	a.mu.Lock()
	return true
}
func (r *workMemoryRuntime) recordThread(instance string, event agentproto.Event) bool {
	root, err := openWorkMemoryRoot(r.root)
	if os.IsNotExist(err) {
		return workMemoryRootMayBeAbsent(r.root)
	}
	if err != nil {
		return false
	}
	defer root.Close()
	owners, err := readWorkMemoryOwners(root)
	if err != nil {
		return false
	}

	owner, ok := owners.Pending[r.lifecycle+":"+event.CommandID]
	if !ok {
		return true
	}
	if owner.Instance != instance || event.Initiator.Kind != agentproto.InitiatorRemoteSurface || owner.Surface != event.Initiator.SurfaceSessionID {
		return false
	}
	if existing, ok := owners.Threads[event.ThreadID]; ok && (existing.Surface != owner.Surface || existing.User != owner.User) {
		return false
	}
	owner.KnownThreads = nil
	owners.Threads[event.ThreadID] = owner
	delete(owners.Pending, r.lifecycle+":"+event.CommandID)
	return writeWorkMemoryOwners(r.root, owners) == nil
}
