package daemon

import (
	"fmt"
	"os"
	"path/filepath"
)

const workMemoryActivationFile = "work-memory-privacy-enabled.json"

type workMemoryActivation struct {
	SchemaVersion int `json:"schema_version"`
}

// Kept outside the collector directory so deleting snapshots cannot erase the
// evidence that native threads may already contain private material.
func workMemoryWasActivated(rootPath string) (bool, error) {
	parent, err := os.OpenRoot(filepath.Dir(rootPath))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer parent.Close()
	var marker workMemoryActivation
	err = workMemoryReadJSON(parent, workMemoryActivationFile, 1024, &marker)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if marker.SchemaVersion != 1 {
		return false, fmt.Errorf("invalid privacy activation")
	}
	return true, nil
}
func initializeWorkMemoryPrivacy(rootPath string) error {
	active, err := workMemoryWasActivated(rootPath)
	if err != nil || active {
		return err
	}
	root, err := openWorkMemoryRoot(rootPath)
	if err != nil {
		return err
	}
	_, statErr := root.Lstat("private-threads.json")
	if os.IsNotExist(statErr) {
		err = writeWorkMemoryOwnersAtomic(rootPath, workMemoryOwners{SchemaVersion: 1, Threads: map[string]workMemoryOwner{}, Pending: map[string]workMemoryOwner{}})
	} else if statErr != nil {
		err = statErr
	} else {
		_, err = readWorkMemoryOwners(root)
	}
	root.Close()
	if err != nil {
		return err
	}
	path := filepath.Join(filepath.Dir(rootPath), workMemoryActivationFile)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		_, err = workMemoryWasActivated(rootPath)
		return err
	}
	if err != nil {
		return err
	}
	_, err = file.WriteString("{\"schema_version\":1}\n")
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func workMemoryRootMayBeAbsent(rootPath string) bool {
	active, err := workMemoryWasActivated(rootPath)
	return err == nil && !active
}
