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
package cache

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestNewListWatchFromMethods(t *testing.T) {
	tt := []struct {
		succeed bool
		obj     interface{}
	}{
		{
			succeed: false,
			obj:     nil,
		},
		{
			succeed: false,
			obj:     clientsetfake.NewSimpleClientset().CoreV1(),
		},
		{
			succeed: true,
			obj:     clientsetfake.NewSimpleClientset().CoreV1().ReplicationControllers(""),
		},
	}

	fieldSelector := fields.OneTermEqualSelector("metadata.name", "test")
	for _, tmptc := range tt {
		tc := tmptc // we need to copy it for parallel runs
		t.Run("", func(t *testing.T) {

			r := func() (i interface{}) {
				defer func() { i = recover() }()

				lw := NewListWatchFromMethods(tc.obj, fieldSelector)
				options := metav1.ListOptions{}
				_, _ = lw.List(options)
				_, _ = lw.Watch(options)

				return nil
			}()

			if r == nil && !tc.succeed {
				t.Fatalf("Should have failed for object: %#v", tc.obj)
			}

			if r != nil && tc.succeed {
				t.Fatalf("Failed with error: %v; object: %#v", r, tc.obj)
			}
		})
	}
}
