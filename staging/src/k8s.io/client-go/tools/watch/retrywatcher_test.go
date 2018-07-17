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
	"errors"
	"flag"
	"fmt"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

type testObject struct {
	resourceVersion string
}

func (o testObject) GetObjectKind() schema.ObjectKind { return schema.EmptyObjectKind }
func (o testObject) DeepCopyObject() runtime.Object   { return o }
func (o testObject) GetResourceVersion() string       { return o.resourceVersion }

func withCounter(f WatcherFunc) (*uint32, func(sinceResourceVersion string) (watch.Interface, error)) {
	var counter uint32
	wrapped := func(sinceResourceVersion string) (watch.Interface, error) {
		atomic.AddUint32(&counter, 1)
		return f(sinceResourceVersion)
	}

	return &counter, wrapped
}

func makeTestEvent(rv int) watch.Event {
	return watch.Event{
		Type: watch.Added,
		Object: testObject{
			resourceVersion: fmt.Sprintf("%d", rv),
		},
	}
}

func AtoiOrDie(s string) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		panic(fmt.Errorf("failed to convert string %q to integer: %v", s, err))
	}
	return i
}

func arrayToChannel(array []watch.Event) chan watch.Event {
	ch := make(chan watch.Event, len(array))

	for _, event := range array {
		ch <- event
	}

	return ch
}

func fromRV(resourceVersion string, array []watch.Event) []watch.Event {
	if resourceVersion == "" {
		return array
	}

	var result []watch.Event
	rv := AtoiOrDie(resourceVersion)

	for _, event := range array {
		if event.Type != watch.Error {
			rvGetter, ok := event.Object.(resourceVersionGetter)
			if ok {
				if AtoiOrDie(rvGetter.GetResourceVersion()) <= rv {
					continue
				}
			}
		}

		result = append(result, event)
	}

	return result
}

func closeAfterN(n int, source chan watch.Event) chan watch.Event {
	result := make(chan watch.Event, 0)
	go func() {
		defer close(result)
		defer close(source)
		for i := 0; i < n; i++ {
			result <- <-source
		}
	}()
	return result
}

func TestRetryWatcher(t *testing.T) {
	// Enable glog which is used in dependencies
	flag.Set("logtostderr", "true")
	flag.Set("v", "9")

	tt := []struct {
		name        string
		initialRV   string
		watcherFunc WatcherFunc
		watchCount  uint32
		result      []watch.Event
		err         runtime.Object
	}{
		{
			name: "fails if watcher returns error",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return nil, fmt.Errorf("test error")
			},
			watchCount: 1,
			result:     []watch.Event{},
			err:        apierrors.NewInternalError(errors.New("RetryWatcher: watcherFunc failed: test error")).Status(),
		},
		{
			name:      "works with empty initialRV",
			initialRV: "",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(1),
				}))), nil
			},
			watchCount: 1,
			result: []watch.Event{
				makeTestEvent(1),
			},
			err: nil,
		},
		{
			name:      "works with initialRV set, skipping the preceding items but reading those directly following",
			initialRV: "1",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(1),
					makeTestEvent(2),
				}))), nil
			},
			watchCount: 1,
			result: []watch.Event{
				makeTestEvent(2),
			},
			err: nil,
		},
		{
			name:      "works with initialRV set, skipping the preceding items with none following",
			initialRV: "3",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(2),
				}))), nil
			},
			watchCount: 1,
			result:     nil,
			err:        nil,
		},
		{
			name:      "fails on RetryWatcherError",
			initialRV: "3",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(4),
					{Type: watch.Error, Object: apierrors.NewInternalError(errors.New("error")).Status()},
				}))), nil
			},
			watchCount: 1,
			result: []watch.Event{
				makeTestEvent(4),
			},
			err: apierrors.NewInternalError(errors.New("error")).Status(),
		},
		{
			name:      "fails on RetryWatcherError, without reading following events",
			initialRV: "5",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(5),
					makeTestEvent(6),
					{Type: watch.Error, Object: apierrors.NewInternalError(errors.New("error")).Status()},
					makeTestEvent(7),
					makeTestEvent(8),
				}))), nil
			},
			watchCount: 1,
			result: []watch.Event{
				makeTestEvent(6),
			},
			err: apierrors.NewInternalError(errors.New("error")).Status(),
		},
		{
			name:      "survives 1 closed watch and reads 1 item",
			initialRV: "5",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(closeAfterN(1, arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(6),
				})))), nil
			},
			watchCount: 2,
			result: []watch.Event{
				makeTestEvent(6),
			},
			err: nil,
		},
		{
			name:      "survives 2 closed watches and reads 2 items",
			initialRV: "4",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(closeAfterN(1, arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(5),
					makeTestEvent(6),
				})))), nil
			},
			watchCount: 3,
			result: []watch.Event{
				makeTestEvent(5),
				makeTestEvent(6),
			},
			err: nil,
		},
		{
			name:      "survives 2 closed watches and reads 2 items for nonconsecutive RVs",
			initialRV: "4",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(closeAfterN(1, arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(5),
					makeTestEvent(7),
				})))), nil
			},
			watchCount: 3,
			result: []watch.Event{
				makeTestEvent(5),
				makeTestEvent(7),
			},
			err: nil,
		},
		{
			name:      "survives 2 closed watches and reads 2 items for nonconsecutive RVs starting at much lower RV",
			initialRV: "2",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(closeAfterN(1, arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(5),
					makeTestEvent(7),
				})))), nil
			},
			watchCount: 3,
			result: []watch.Event{
				makeTestEvent(5),
				makeTestEvent(7),
			},
			err: nil,
		},
		{
			name:      "survives 4 closed watches and reads 4 items for nonconsecutive, spread RVs",
			initialRV: "2",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(closeAfterN(1, arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(5),
					makeTestEvent(6),
					makeTestEvent(7),
					makeTestEvent(11),
				})))), nil
			},
			watchCount: 5,
			result: []watch.Event{
				makeTestEvent(5),
				makeTestEvent(6),
				makeTestEvent(7),
				makeTestEvent(11),
			},
			err: nil,
		},
		{
			name:      "survives 4 closed watches and reads 4 items for nonconsecutive, spread RVs and skips those with lower or equal RV",
			initialRV: "2",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(closeAfterN(1, arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(1),
					makeTestEvent(2),
					makeTestEvent(5),
					makeTestEvent(6),
					makeTestEvent(7),
					makeTestEvent(11),
				})))), nil
			},
			watchCount: 5,
			result: []watch.Event{
				makeTestEvent(5),
				makeTestEvent(6),
				makeTestEvent(7),
				makeTestEvent(11),
			},
			err: nil,
		},
		{
			name:      "survives 2 closed watches and reads 2+2+1 items skipping those with equal RV",
			initialRV: "1",
			watcherFunc: func(sinceResourceVersion string) (watch.Interface, error) {
				return watch.NewProxyWatcher(closeAfterN(2, arrayToChannel(fromRV(sinceResourceVersion, []watch.Event{
					makeTestEvent(1),
					makeTestEvent(2),
					makeTestEvent(5),
					makeTestEvent(6),
					makeTestEvent(7),
					makeTestEvent(11),
				})))), nil
			},
			watchCount: 3,
			result: []watch.Event{
				makeTestEvent(2),
				makeTestEvent(5),
				makeTestEvent(6),
				makeTestEvent(7),
				makeTestEvent(11),
			},
			err: nil,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			counter, watchFunc := withCounter(tc.watcherFunc)
			watcher := NewRetryWatcher(tc.initialRV, watchFunc)

			var result []watch.Event
			event := watch.Event{Type: watch.Added}
		loop:
			for {
				select {
				case e, ok := <-watcher.ResultChan():
					if !ok {
						t.Errorf("Failed to read event: channel is already closed!")
						return
					}

					if event.Type == watch.Error {
						t.Errorf("received event %#v after error: %#v", e, event)
						return
					}

					event = e

					result = append(result, event)
				case <-time.After(time.Second * 1):
					// All the events are buffered -> this means we are done
					// Also the one sec will make sure that we would detect RetryWatcher's incorrect behaviour after last event
					watcher.Stop()
					break loop
				}

				if event.Type == watch.Error {
					continue
				}
			}

			if event.Type == watch.Error {
				if !reflect.DeepEqual(event.Object, tc.err) {
					t.Fatalf("expected error %#v, got %#v", tc.err, event.Object)
				}

				if len(result) >= 1 {
					result = result[:len(result)-1]
				}
			} else {
				if tc.err != nil {
					t.Errorf("expected error %v, but succeeded", tc.err)
					return
				}
			}

			if atomic.LoadUint32(counter) != tc.watchCount {
				t.Errorf("expected %d watcher starts, but it has started %d times", tc.watchCount, *counter)
			}

			if !reflect.DeepEqual(result, tc.result) {
				t.Errorf("\nexpected: %#v,\ngot:      %#v", tc.result, result)
			}
		})
	}
}
