package informer

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	testcore "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/apis/core"
	fakeclientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset/fake"
	apiequality "k8s.io/kubernetes/staging/src/k8s.io/apimachinery/pkg/api/equality"
)

func TestNewInformerWatcher(t *testing.T) {
	objects := []runtime.Object{
		&core.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-1",
			},
		},
		&core.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-2",
			},
		},
		&core.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-3",
			},
		},
	}

	fake := fakeclientset.NewSimpleClientset(objects...)
	fakeWatch := watch.NewFakeWithChanSize(len(objects), false)
	fake.PrependWatchReactor("pods", testcore.DefaultWatchReactor(fakeWatch, nil))

	fakeWatch.Add(objects[0])
	fakeWatch.Modify(objects[1])
	fakeWatch.Delete(objects[2])

	lw := cache.NewListWatchFromMethods(fake.Core().Pods(""), fields.Everything())
	w := NewInformerWatcher(lw, &core.Pod{}, 0)

	for i := 0; i < 2; i++ {
		res := <-w.ResultChan()
		// FIXME 1->i
		if !apiequality.Semantic.DeepEqual(res.Object, objects[0]) {
			t.Errorf("%d: got %#v, expected: %#v", i, res.Object, objects[i])
		}
	}

	// Stop before reading all the data to make sure the informer can deal with closed channel
	w.Stop()

	// Wait a bit to see if the informer won't panic
	time.Sleep(1 * time.Second)
}
