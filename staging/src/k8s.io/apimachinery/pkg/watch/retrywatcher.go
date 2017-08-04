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

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Watch function is responsible for creating a watcher starting at sinceResourceVersion.
type WatchFunc func(sinceResourceVersion string) Interface

// Type of an event that will be returned if RetryWatcher fails
type RetryWatcherError string

// Implements interface.
func (obj RetryWatcherError) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }

// Implements interface.
func (obj RetryWatcherError) DeepCopyObject() runtime.Object { return obj }

// Interface to get resource version from events.
// We can't reuse an interface from meta otherwise it would be a cyclic dependency and we need just this one method
type resourceVersionInterface interface {
	GetResourceVersion() string
}

// Watchers can be closed e.g. in case API timeout is reached, etcd timeout, ...
// RetryWatcher will make sure that in case the watcher is closed it will get restarted
// from the last point without the consumer even knowing about it. RetryWatcher does that
// by inspecting events and keeping track of resourceVersion.
// Especially useful when using watch.Until where premature termination is causing troubles and flakes.
// Please note that this is not resilient to ETCD cache not having the resource version anymore - you would need to use Informers for that.
type RetryWatcher struct {
	lastResourceVersion string
	watchFunc           WatchFunc
	resultChan          chan Event
	stopChan            chan struct{}
}

// Creates a new RetryWatcher.
func NewRetryWatcher(initialResourceVersion string, watchFunc WatchFunc) *RetryWatcher {
	rw := &RetryWatcher{
		lastResourceVersion: initialResourceVersion,
		watchFunc:           watchFunc,
		stopChan:            make(chan struct{}),
		resultChan:          make(chan Event, 1),
	}
	go rw.receive()
	return rw
}

// receive reads the result from a watcher, restarting it if necessary.
func (rw *RetryWatcher) receive() {
	for {
		// We need to wrap this code in a function so the old watchers are freed by defer between retries
		done := func() bool {
			watcher := rw.watchFunc(rw.lastResourceVersion)
			ch := watcher.ResultChan()
			defer watcher.Stop()

			select {
			case event, ok := <-ch:
				if !ok {
					return false
				}

				// We need to inspect the event and get ResourceVersion out of it
				switch t := event.Type; t {
				case Added, Modified, Deleted:
					metaObject, ok := event.Object.(resourceVersionInterface)
					if !ok {
						rw.resultChan <- Event{
							Type:   Error,
							Object: RetryWatcherError("__internal__: RetryWatcher: doen't support resourceversion"),
						}
						// We have to abort here because this might cause lastResourceVersion inconsistency!
						return true
					}

					resourceVersion := metaObject.GetResourceVersion()
					if resourceVersion == "" {
						rw.resultChan <- Event{
							Type:   Error,
							Object: RetryWatcherError(fmt.Sprintf("__internal__: RetryWatcher: object %#v doesn't support resourceVersion", event.Object)),
						}
						// We have to abort here because this might cause lastResourceVersion inconsistency!
						return true
					}

					// All is fine; update lastResourceVersion and send the event
					rw.lastResourceVersion = resourceVersion
					rw.resultChan <- event

					return false

				case Error:
					rw.resultChan <- event
					// TODO: check if there is a reasonable error to retry here
					return true

				default:
					rw.resultChan <- Event{
						Type:   Error,
						Object: RetryWatcherError(fmt.Sprintf("__internal__: RetryWatcher failed to recognize Event type %q", t)),
					}
					// We have to abort here because this might cause lastResourceVersion inconsistency!
					return true
				}
			case <-rw.stopChan:
				return true
			}
		}()

		if done {
			break
		}
	}
}

// ResultChan implements Interface.
func (rw *RetryWatcher) ResultChan() <-chan Event {
	return rw.resultChan
}

// Stop implements Interface.
func (rw *RetryWatcher) Stop() {
	close(rw.stopChan)
}

// WithRetry will wrap the watchFunc with RetryWatcher making sure that watcher gets restarted in case of errors.
// The initialResourceVersion will be given to watchFunc when first called.
func WithRetry(initialResourceVersion string, watchFunc WatchFunc) Interface {
	return NewRetryWatcher(initialResourceVersion, watchFunc)
}
