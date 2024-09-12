package broadcaster

import (
	"errors"
	"sync"
)

type LocalMutex struct {
	mutex sync.Mutex
}

func (l *LocalMutex) Lock() error {
	if l.mutex.TryLock() {
		return nil
	}
	return errors.New("lock failed")
}

func (l *LocalMutex) Unlock() (bool, error) {
	l.mutex.Unlock()
	return true, nil
}
