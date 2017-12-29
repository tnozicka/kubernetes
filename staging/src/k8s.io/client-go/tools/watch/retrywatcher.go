/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package watch

import (
	"fmt"

	"github.com/golang/glog"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

// WatcherFunc is a function that is responsible for creating a watcher starting at sinceResourceVersion.
type WatcherFunc func(sinceResourceVersion string) watch.Interface

// RetryWatcherError is the type of an event that will be returned if RetryWatcher fails.
type RetryWatcherError struct {
	s string
}

// GetObjectKind implements error interface.
func (e RetryWatcherError) Error() string {
	return e.s
}

// GetObjectKind implements runtime.Object interface.
func (e RetryWatcherError) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }

// DeepCopyObject implements runtime.Object interface.
func (e RetryWatcherError) DeepCopyObject() runtime.Object { return e }

var _ error = RetryWatcherError{}
var _ runtime.Object = RetryWatcherError{}

// resourceVersionGetter is an interface used to get resource version from events.
// We can't reuse an interface from meta otherwise it would be a cyclic dependency and we need just this one method
type resourceVersionGetter interface {
	GetResourceVersion() string
}

// RetryWatcher will make sure that in case the underlying watcher is closed (e.g. due to API timeout or etcd timeout)
// it will get restarted from the last point without the consumer even knowing about it.
// RetryWatcher does that by inspecting events and keeping track of resourceVersion.
// Especially useful when using watch.Until where premature termination is causing troubles and flakes.
// Please note that this is not resilient to ETCD cache not having the resource version anymore - you would need to
// use Informers for that.
type RetryWatcher struct {
	lastResourceVersion string
	watcherFunc         WatcherFunc
	resultChan          chan watch.Event
	stopChan            chan struct{}
}

// NewRetryWatcher creates a new RetryWatcher.
// It will make sure that watcher gets restarted in case of recoverable errors.
// The initialResourceVersion will be given to watchFunc when first called.
func NewRetryWatcher(initialResourceVersion string, watcherFunc WatcherFunc) *RetryWatcher {
	rw := &RetryWatcher{
		lastResourceVersion: initialResourceVersion,
		watcherFunc:         watcherFunc,
		stopChan:            make(chan struct{}),
		resultChan:          make(chan watch.Event, 0),
	}
	go rw.receive()
	return rw
}

// doReceive returns true when it is done, false otherwise
func (rw *RetryWatcher) doReceive() bool {
	watcher := rw.watcherFunc(rw.lastResourceVersion)
	if watcher == nil {
		rw.resultChan <- watch.Event{
			Type:   watch.Error,
			Object: RetryWatcherError{fmt.Sprintf("RetryWatcher failed: watcherFunc returned nil watcher!")},
		}
		// Stop the watcher
		return true
	}
	ch := watcher.ResultChan()
	defer watcher.Stop()

	for {
		select {
		case <-rw.stopChan:
			glog.Info("Stopping RetryWatcher.")
			return true
		case event, ok := <-ch:
			if !ok {
				glog.Warningf("RetryWatcher - getting event failed! Re-creating the watcher. Last RV: %s", rw.lastResourceVersion)
				return false
			}

			// We need to inspect the event and get ResourceVersion out of it
			switch event.Type {
			case watch.Added, watch.Modified, watch.Deleted:
				metaObject, ok := event.Object.(resourceVersionGetter)
				if !ok {
					rw.resultChan <- watch.Event{
						Type:   watch.Error,
						Object: RetryWatcherError{"__internal__: RetryWatcher: doesn't support resourceVersion"},
					}
					// We have to abort here because this might cause lastResourceVersion inconsistency!
					return true
				}

				resourceVersion := metaObject.GetResourceVersion()
				if resourceVersion == "" {
					rw.resultChan <- watch.Event{
						Type:   watch.Error,
						Object: RetryWatcherError{fmt.Sprintf("__internal__: RetryWatcher: object %#v doesn't support resourceVersion", event.Object)},
					}
					// We have to abort here because this might cause lastResourceVersion inconsistency!
					return true
				}

				// All is fine; send the event and update lastResourceVersion
				rw.resultChan <- event
				rw.lastResourceVersion = resourceVersion

				continue

			case watch.Error:
				rw.resultChan <- event
				// TODO: check if there is a reasonable error to retry here
				return true

			default:
				rw.resultChan <- watch.Event{
					Type:   watch.Error,
					Object: RetryWatcherError{fmt.Sprintf("__internal__: RetryWatcher failed to recognize Event type %q", event.Type)},
				}
				// We have to abort here because this might cause lastResourceVersion inconsistency!
				return true
			}
		}
	}
}

// receive reads the result from a watcher, restarting it if necessary.
func (rw *RetryWatcher) receive() {
	for {
		select {
		case <-rw.stopChan:
			glog.Info("Stopping RetryWatcher.")
			return
		default:
			done := rw.doReceive()
			if done {
				return
			}
		}
	}
}

// ResultChan implements Interface.
func (rw *RetryWatcher) ResultChan() <-chan watch.Event {
	return rw.resultChan
}

// Stop implements Interface.
func (rw *RetryWatcher) Stop() {
	close(rw.stopChan)
}
