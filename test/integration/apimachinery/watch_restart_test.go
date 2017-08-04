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

package apimachinery

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/test/integration/framework"
)

func TestWatchRestartsIfTimeoutNotReached(t *testing.T) {
	// Has to be longer than 5 seconds
	timeout := 2 * time.Minute

	// Set up a master
	masterConfig := framework.NewIntegrationTestMasterConfig()
	masterConfig.EnableCoreControllers = false
	// Timeout is set random between MinRequestTimeout and 2x
	masterConfig.GenericConfig.MinRequestTimeout = int(timeout.Seconds()) / 4
	_, s, closeFn := framework.RunAMaster(masterConfig)
	defer closeFn()

	gv := &api.Registry.GroupOrDie(v1.GroupName).GroupVersion
	config := &restclient.Config{
		Host:          s.URL,
		ContentConfig: restclient.ContentConfig{GroupVersion: gv},
	}

	namespaceObject := framework.CreateTestingNamespace("retry-watch", s, t)
	defer framework.DeleteTestingNamespace(namespaceObject, s, t)

	getWatchFunc := func(c *kubernetes.Clientset, secret *v1.Secret) func(rv string) watch.Interface {
		return func(rv string) watch.Interface {
			watcher, err := c.CoreV1().Secrets(secret.Namespace).Watch(metav1.SingleObject(metav1.ObjectMeta{Name: secret.Name, ResourceVersion: rv}))
			if err != nil {
				t.Fatalf("Failed to create watcher on Secret: %v", err)
			}
			return watcher
		}
	}

	generateEvents := func(t *testing.T, c *kubernetes.Clientset, secret *v1.Secret, referenceOutput *[]string, stopChan chan struct{}, stoppedChan chan struct{}) {
		defer close(stoppedChan)
		counter := 0

		for {
			select {
			// TODO: get this lower once we figure out how to extend ETCD cache
			case <-time.After(1000 * time.Millisecond):
				counter = counter + 1

				patch := fmt.Sprintf(`{"metadata": {"annotations": {"count": "%d"}}}`, counter)
				_, err := c.CoreV1().Secrets(secret.Namespace).Patch(secret.Name, types.StrategicMergePatchType, []byte(patch))
				if err != nil {
					t.Fatalf("Failed to patch secret: %v", err)
				}

				*referenceOutput = append(*referenceOutput, fmt.Sprintf("%d", counter))
			// These 5 seconds are here to protect against a race at the end when we could write something there at the same time as watch.Until ends
			case <-time.After(timeout - 5*time.Second):
				if timeout-5*time.Second < 0 {
					panic("Timeout has to be grater than 5 seconds!")
				}
				return
			case <-stopChan:
				return
			}
		}
	}

	newTestSecret := func(name string) *v1.Secret {
		return &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespaceObject.Name,
				Annotations: map[string]string{
					"count": "0",
				},
			},
			Data: map[string][]byte{
				"data": []byte("value1\n"),
			},
		}
	}

	tt := []struct {
		succeed    bool
		secret     *v1.Secret
		getWatcher func(c *kubernetes.Clientset, secret *v1.Secret) watch.Interface
	}{
		{
			succeed: false,
			secret:  newTestSecret("secret-01"),
			getWatcher: func(c *kubernetes.Clientset, secret *v1.Secret) watch.Interface {
				return getWatchFunc(c, secret)(secret.ResourceVersion)
			}, // regular watcher; unfortunately destined to fail
		},
		{
			succeed: true,
			secret:  newTestSecret("secret-02"),
			getWatcher: func(c *kubernetes.Clientset, secret *v1.Secret) watch.Interface {
				return watch.WithRetry(secret.ResourceVersion, getWatchFunc(c, secret))
			},
		},
	}
	for _, tmptc := range tt {
		tc := tmptc // we need to copy it for parallel runs
		t.Run("", func(t *testing.T) {
			// TODO: enable parallel run because these test take long
			//t.Parallel() // There is something wrong with the server this way - says: "getsockopt: connection refused"

			c, err := kubernetes.NewForConfig(config)
			if err != nil {
				t.Fatalf("Failed to create clientset: %v", err)
			}

			secret, err := c.CoreV1().Secrets(tc.secret.Namespace).Create(tc.secret)
			if err != nil {
				t.Fatalf("Failed to create testing secret %s/%s: %v", tc.secret.Namespace, tc.secret.Name, err)
			}

			var referenceOutput []string
			var output []string
			watcher := tc.getWatcher(c, secret)
			stopChan := make(chan struct{})
			stoppedChan := make(chan struct{})
			go generateEvents(t, c, secret, &referenceOutput, stopChan, stoppedChan)

			// Record current time to be able to asses if the timeout has been reached
			startTime := time.Now()
			_, err = watch.Until(timeout, watcher, func(event watch.Event) (bool, error) {
				s, ok := event.Object.(*v1.Secret)
				if !ok {
					t.Fatalf("Recived an object that is not a Secret: %#v", event.Object)
				}
				output = append(output, s.Annotations["count"])
				// Watch will never end voluntarily
				return false, nil
			})
			watchDuration := time.Since(startTime)
			close(stopChan)
			<-stoppedChan

			t.Logf("Watch duration: %v; timeout: %v", watchDuration, timeout)

			if err == nil && !tc.succeed {
				t.Fatalf("Watch should have timed out but it exited without an error!")
			}

			if err != wait.ErrWaitTimeout && tc.succeed {
				t.Fatalf("Watch exited with error: %v!", err)
			}

			if watchDuration < timeout && tc.succeed {
				t.Fatalf("Watch should have timed out after %v but it timed out prematurely after %v!", timeout, watchDuration)
			}

			if watchDuration >= timeout && !tc.succeed {
				t.Fatalf("Watch should have timed out but it succeeded!")
			}

			if tc.succeed && !reflect.DeepEqual(referenceOutput, output) {
				t.Fatalf("Reference and real output differ! We must have lost some events or read some multiple times!\nRef:  %#v\nReal: %#v", referenceOutput, output)
			}
		})
	}
}
