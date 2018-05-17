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
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// NewInformerWatcher will create an IndexedInformer and wrap it into watch.Interface
// so you can use it anywhere where you'd have used a regular Watcher returned from Watch method.
func NewInformerWatcher(lw cache.ListerWatcher, objType runtime.Object, resyncPeriod time.Duration) watch.Interface {
	ch := make(chan watch.Event)
	w := watch.NewProxyWatcher(ch)

	_, informer := cache.NewIndexerInformer(lw, objType, resyncPeriod, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ch <- watch.Event{
				Type:   watch.Added,
				Object: obj.(runtime.Object),
			}
		},
		UpdateFunc: func(old, new interface{}) {
			ch <- watch.Event{
				Type:   watch.Modified,
				Object: new.(runtime.Object),
			}
		},
		DeleteFunc: func(obj interface{}) {
			ch <- watch.Event{
				Type:   watch.Deleted,
				Object: obj.(runtime.Object),
			}
		},
	}, cache.Indexers{})

	go func() {
		informer.Run(w.StopChan())
		close(ch)
	}()

	return w
}
