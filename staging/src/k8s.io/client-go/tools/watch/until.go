/*
Copyright 2016 The Kubernetes Authors.

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
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// ConditionFunc returns true if the condition has been reached, false if it has not been reached yet,
// or an error if the condition cannot be checked and should terminate. In general, it is better to define
// level driven conditions over edge driven conditions (pod has ready=true, vs pod modified and ready changed
// from false to true).
type ConditionFunc func(event watch.Event) (bool, error)

// ErrWatchClosed is returned when the watch channel is closed before timeout in UntilWithoutRetry.
var ErrWatchClosed = errors.New("watch closed before UntilWithoutRetry timeout")

// UntilWithoutRetry reads items from the watch until each provided condition succeeds, and then returns the last watch
// encountered. The first condition that returns an error terminates the watch (and the event is also returned).
// If no event has been received, the returned event will be nil.
// Conditions are satisfied sequentially so as to provide a useful primitive for higher level composition.
// A zero timeout means to wait forever.
//
// Warning: Unless you have a very specific use case (probably a special Watcher) don't use this function!!!
// Warning: This will fail e.g. on API timeouts and/or 'too old resource version' error.
// Warning: You are most probably looking for a function *UntilWithRetry* or *UntilWithInformer* bellow,
// Warning: solving such issues.
func UntilWithoutRetry(timeout time.Duration, watcher watch.Interface, conditions ...ConditionFunc) (*watch.Event, error) {
	ch := watcher.ResultChan()
	defer watcher.Stop()
	var after <-chan time.Time
	if timeout > 0 {
		after = time.After(timeout)
	} else {
		ch := make(chan time.Time)
		defer close(ch)
		after = ch
	}
	var lastEvent *watch.Event
	for _, condition := range conditions {
		// check the next condition against the previous event and short circuit waiting for the next watch
		if lastEvent != nil {
			done, err := condition(*lastEvent)
			if err != nil {
				return lastEvent, err
			}
			if done {
				continue
			}
		}
	ConditionSucceeded:
		for {
			select {
			case event, ok := <-ch:
				if !ok {
					return lastEvent, ErrWatchClosed
				}
				lastEvent = &event

				done, err := condition(event)
				if err != nil {
					return lastEvent, err
				}
				if done {
					break ConditionSucceeded
				}

			case <-after:
				return lastEvent, wait.ErrWaitTimeout
			}
		}
	}
	return lastEvent, nil
}

// UntilWithRetry wraps the watchFunc with RetryWatcher making sure that watcher gets restarted in case of errors.
// The initialResourceVersion will be given to watchFunc when first called.
// Remaining behaviour is identical to function UntilWithoutRetry. (See above.)
// UntilWithRetry can deal with API timeout or lost connections.
// It guarantees you to see all events and in the order they happened.
// Due to this guarantee there is no way it can deal with 'Resource version too old error'. It will fail in this case.
// The most frequent usage would be a test where you want to verify exact order of events.
func UntilWithRetry(timeout time.Duration, initialResourceVersion string, watcherFunc WatcherFunc, conditions ...ConditionFunc) (*watch.Event, error) {
	return UntilWithoutRetry(timeout, NewRetryWatcher(initialResourceVersion, watcherFunc), conditions...)
}

// UntilWithInformer creates an informer from lw and watches it's output until each provided condition succeeds,
// in a way that is identical to function UntilWithoutRetry. (See above.)
// UntilWithInformer can deal with all errors like API timeout, lost connections or 'Resource version too old'.
// On the other hand it can't provide you with guarantees as strong as UntilWithRetry. It can miss some events
// in case of watch function failing but it will re-list to recover.
// The most frequent usage would be a command that needs to watch the "state of the world" and should't fail. ("small" controllers)
func UntilWithInformer(timeout time.Duration, lw cache.ListerWatcher, objType runtime.Object, resyncPeriod time.Duration, conditions ...ConditionFunc) (*watch.Event, error) {
	return UntilWithoutRetry(timeout, NewInformerWatcher(lw, objType, resyncPeriod), conditions...)
}
