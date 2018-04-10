/*
Copyright 2015 The Kubernetes Authors.

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

package cache

import (
	"context"
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/pager"
)

// Lister is any object that knows how to perform an initial list.
type Lister interface {
	// List should return a list type object; the Items field will be extracted, and the
	// ResourceVersion field will be used to start the watch in the right place.
	List(options metav1.ListOptions) (runtime.Object, error)
}

// Watcher is any object that knows how to start a watch on a resource.
type Watcher interface {
	// Watch should begin a watch at the specified version.
	Watch(options metav1.ListOptions) (watch.Interface, error)
}

// ListerWatcher is any object that knows how to perform an initial list and start a watch on a resource.
type ListerWatcher interface {
	Lister
	Watcher
}

// ListFunc knows how to list resources
type ListFunc func(options metav1.ListOptions) (runtime.Object, error)

// WatchFunc knows how to watch resources
type WatchFunc func(options metav1.ListOptions) (watch.Interface, error)

// ListWatch knows how to list and watch a set of apiserver resources.  It satisfies the ListerWatcher interface.
// It is a convenience function for users of NewReflector, etc.
// ListFunc and WatchFunc must not be nil
type ListWatch struct {
	ListFunc  ListFunc
	WatchFunc WatchFunc
	// DisableChunking requests no chunking for this list watcher.
	DisableChunking bool
}

// Getter interface knows how to access Get method from RESTClient.
type Getter interface {
	Get() *restclient.Request
}

// NewListWatchFromClient creates a new ListWatch from the specified client, resource, namespace and field selector.
func NewListWatchFromClient(c Getter, resource string, namespace string, fieldSelector fields.Selector) *ListWatch {
	optionsModifier := func(options *metav1.ListOptions) {
		options.FieldSelector = fieldSelector.String()
	}
	return NewFilteredListWatchFromClient(c, resource, namespace, optionsModifier)
}

// NewFilteredListWatchFromClient creates a new ListWatch from the specified client, resource, namespace, and option modifier.
// Option modifier is a function takes a ListOptions and modifies the consumed ListOptions. Provide customized modifier function
// to apply modification to ListOptions with a field selector, a label selector, or any other desired options.
func NewFilteredListWatchFromClient(c Getter, resource string, namespace string, optionsModifier func(options *metav1.ListOptions)) *ListWatch {
	listFunc := func(options metav1.ListOptions) (runtime.Object, error) {
		optionsModifier(&options)
		return c.Get().
			Namespace(namespace).
			Resource(resource).
			VersionedParams(&options, metav1.ParameterCodec).
			Do().
			Get()
	}
	watchFunc := func(options metav1.ListOptions) (watch.Interface, error) {
		options.Watch = true
		optionsModifier(&options)
		return c.Get().
			Namespace(namespace).
			Resource(resource).
			VersionedParams(&options, metav1.ParameterCodec).
			Watch()
	}
	return &ListWatch{ListFunc: listFunc, WatchFunc: watchFunc}
}

// applyUserListOptions applies only those options that are allowed to be set in List and Watch for an Informer
func applyUserListOptions(options metav1.ListOptions, to *metav1.ListOptions) {
	/*
	 * Can't copy:
	 * - ResourceVersion
	 * - Timeout
	 * as these are used by informers internally
	 */

	to.LabelSelector = options.LabelSelector
	to.FieldSelector = options.FieldSelector
	to.IncludeUninitialized = options.IncludeUninitialized
}

// NewListWatchFromMethods creates a ListWatch object from a client supporting List and Watch methods.
// Because Golang doesn't have templates/generics to allow us to accept an object implementing List+Watch interface but differing
// in return types the client's type is erased to in interface{} and checked in runtime using reflection.
func NewListWatchFromMethods(listerWatcher interface{}, userListOptions metav1.ListOptions) *ListWatch {
	v := reflect.ValueOf(listerWatcher)
	t := v.Type()

	listMethod, ok := t.MethodByName("List")
	if !ok {
		panic(fmt.Errorf("missing 'List' method"))
	}
	// parameter #0 is a receiver
	if listMethod.Type.NumIn() != 2 && listMethod.Type.NumOut() != 2 {
		panic(fmt.Errorf(
			"'List' doesn't have 1 input parameters and 2 return values but has %d inputs and %d output values",
			listMethod.Type.NumIn(),
			listMethod.Type.NumOut(),
		))
	}
	if listMethod.Type.In(1) != reflect.TypeOf(metav1.ListOptions{}) {
		panic(fmt.Errorf(
			"'List' method argument must be '%#v' but it's '%#v'",
			reflect.TypeOf(metav1.ListOptions{}).String(),
			listMethod.Type.In(1).String(),
		))
	}

	return &ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			applyUserListOptions(userListOptions, &options)
			results := v.MethodByName("List").Call([]reflect.Value{reflect.ValueOf(options)})

			var resObj runtime.Object
			objInt := results[0].Interface()
			if objInt != nil {
				resObj = objInt.(runtime.Object)
			}

			var resErr error
			errInt := results[1].Interface()
			if errInt != nil {
				resErr = errInt.(error)
			}

			return resObj, resErr
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			applyUserListOptions(userListOptions, &options)
			results := v.MethodByName("Watch").Call([]reflect.Value{reflect.ValueOf(options)})

			var resObj watch.Interface
			objInt := results[0].Interface()
			if objInt != nil {
				resObj = objInt.(watch.Interface)
			}

			var resErr error
			errInt := results[1].Interface()
			if errInt != nil {
				resErr = errInt.(error)
			}

			return resObj, resErr
		},
	}
}

// List lists a set of apiserver resources
func (lw *ListWatch) List(options metav1.ListOptions) (runtime.Object, error) {
	if !lw.DisableChunking {
		return pager.New(pager.SimplePageFunc(lw.ListFunc)).List(context.TODO(), options)
	}
	return lw.ListFunc(options)
}

// Watch watches a set of apiserver resources
func (lw *ListWatch) Watch(options metav1.ListOptions) (watch.Interface, error) {
	return lw.WatchFunc(options)
}
