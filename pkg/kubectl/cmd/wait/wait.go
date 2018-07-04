/*
Copyright 2018 The Kubernetes Authors.

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

package wait

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	watchtools "k8s.io/client-go/tools/watch"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions/printers"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions/resource"
)

// WaitFlags directly reflect the information that CLI is gathering via flags.  They will be converted to Options, which
// reflect the runtime requirements for the command.  This structure reduces the transformation to wiring and makes
// the logic itself easy to unit test
type WaitFlags struct {
	RESTClientGetter     genericclioptions.RESTClientGetter
	PrintFlags           *genericclioptions.PrintFlags
	ResourceBuilderFlags *genericclioptions.ResourceBuilderFlags

	Timeout      time.Duration
	ForCondition string

	genericclioptions.IOStreams
}

// NewWaitFlags returns a default WaitFlags
func NewWaitFlags(restClientGetter genericclioptions.RESTClientGetter, streams genericclioptions.IOStreams) *WaitFlags {
	return &WaitFlags{
		RESTClientGetter: restClientGetter,
		PrintFlags:       genericclioptions.NewPrintFlags("condition met"),
		ResourceBuilderFlags: genericclioptions.NewResourceBuilderFlags().
			WithLabelSelector("").
			WithAllNamespaces(false).
			WithLatest(),

		Timeout: 30 * time.Second,

		IOStreams: streams,
	}
}

// NewCmdWait returns a cobra command for waiting
func NewCmdWait(restClientGetter genericclioptions.RESTClientGetter, streams genericclioptions.IOStreams) *cobra.Command {
	flags := NewWaitFlags(restClientGetter, streams)

	cmd := &cobra.Command{
		Use: "wait resource.group/name [--for=delete|--for condition=available]",
		DisableFlagsInUseLine: true,
		Short: "Experimental: Wait for one condition on one or many resources",
		Run: func(cmd *cobra.Command, args []string) {
			o, err := flags.ToOptions(args)
			cmdutil.CheckErr(err)
			err = o.RunWait()
			cmdutil.CheckErr(err)
		},
		SuggestFor: []string{"list", "ps"},
	}

	flags.AddFlags(cmd)

	return cmd
}

// AddFlags registers flags for a cli
func (flags *WaitFlags) AddFlags(cmd *cobra.Command) {
	flags.PrintFlags.AddFlags(cmd)
	flags.ResourceBuilderFlags.AddFlags(cmd.Flags())

	cmd.Flags().DurationVar(&flags.Timeout, "timeout", flags.Timeout, "The length of time to wait before giving up.  Zero means check once and don't wait, negative means wait for a week.")
	cmd.Flags().StringVar(&flags.ForCondition, "for", flags.ForCondition, "The condition to wait on: [delete|condition=condition-name].")
}

// ToOptions converts from CLI inputs to runtime inputs
func (flags *WaitFlags) ToOptions(args []string) (*WaitOptions, error) {
	printer, err := flags.PrintFlags.ToPrinter()
	if err != nil {
		return nil, err
	}
	builder := flags.ResourceBuilderFlags.ToBuilder(flags.RESTClientGetter, args)
	clientConfig, err := flags.RESTClientGetter.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(clientConfig)
	if err != nil {
		return nil, err
	}
	conditionFn, err := conditionWaitFuncFor(flags.ForCondition)
	if err != nil {
		return nil, err
	}

	effectiveTimeout := flags.Timeout
	if effectiveTimeout < 0 {
		effectiveTimeout = 168 * time.Hour
	}

	o := &WaitOptions{
		ResourceFinder: builder,
		DynamicClient:  dynamicClient,
		Timeout:        effectiveTimeout,

		Printer:     printer,
		ConditionFn: conditionFn,
		IOStreams:   flags.IOStreams,
	}

	return o, nil
}

func conditionWaitFuncFor(condition string) (ConditionFunc, error) {
	if strings.ToLower(condition) == "delete" {
		return IsDeleted, nil
	}
	if strings.HasPrefix(condition, "condition=") {
		conditionName := condition[len("condition="):]
		return ConditionalWait{
			conditionName: conditionName,
			// TODO allow specifying a false
			conditionStatus: "true",
		}.IsConditionMet, nil
	}

	return nil, fmt.Errorf("unrecognized condition: %q", condition)
}

// WaitOptions is a set of options that allows you to wait.  This is the object reflects the runtime needs of a wait
// command, making the logic itself easy to unit test with our existing mocks.
type WaitOptions struct {
	ResourceFinder genericclioptions.ResourceFinder
	DynamicClient  dynamic.Interface
	Timeout        time.Duration

	Printer     printers.ResourcePrinter
	ConditionFn ConditionFunc
	genericclioptions.IOStreams
}

// ConditionFunc is the interface for providing condition checks
type ConditionFunc func(info *resource.Info, o *WaitOptions) (finalObject runtime.Object, done bool, err error)

// RunWait runs the waiting logic
func (o *WaitOptions) RunWait() error {
	return o.ResourceFinder.Do().Visit(func(info *resource.Info, err error) error {
		if err != nil {
			return err
		}
		finalObject, success, err := o.ConditionFn(info, o)
		if success {
			o.Printer.PrintObj(finalObject, o.Out)
			return nil
		}
		if err == nil {
			return fmt.Errorf("%v unsatisified for unknown reason", finalObject)
		}
		return err
	})
}

// IsDeleted is a condition func for waiting for something to be deleted
func InformerWait(info *resource.Info, o *WaitOptions, precondition watchtools.PreconditionFunc, condition watchtools.ConditionFunc) (runtime.Object, bool, error) {
	fieldSelector := fields.OneTermEqualSelector("metadata.name", info.Name).String()
	lw := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.FieldSelector = fieldSelector
			return o.DynamicClient.Resource(info.Mapping.Resource).Namespace(info.Namespace).List(options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = fieldSelector
			return o.DynamicClient.Resource(info.Mapping.Resource).Namespace(info.Namespace).Watch(options)
		},
	}

	event, err := watchtools.UntilWithInformer(o.Timeout, lw, &unstructured.Unstructured{}, 0, precondition, condition)
	if err != nil {
		return info.Object, false, err
	}

	if event == nil {
		return nil, true, nil
	}

	return event.Object, true, nil
}

// IsDeleted is a condition func for waiting for something to be deleted
func IsDeleted(info *resource.Info, o *WaitOptions) (runtime.Object, bool, error) {
	preconditionFunc := func(store cache.Store) (bool, error) {
		_, exists, err := store.GetByKey(fmt.Sprintf("%s/%s", info.Namespace, info.Name))
		if err != nil {
			return true, err
		}
		if !exists {
			return true, nil
		}

		return false, nil
	}

	conditionFunc := func(event watch.Event) (bool, error) {
		return event.Type == watch.Deleted, nil
	}

	return InformerWait(info, o, preconditionFunc, conditionFunc)
}

// ConditionalWait hold information to check an API status condition
type ConditionalWait struct {
	conditionName   string
	conditionStatus string
}

// IsConditionMet is a conditionfunc for waiting on an API condition to be met
func (w ConditionalWait) IsConditionMet(info *resource.Info, o *WaitOptions) (runtime.Object, bool, error) {
	return InformerWait(info, o, nil, w.isConditionMet)
}

func (w ConditionalWait) checkCondition(obj *unstructured.Unstructured) (bool, error) {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	for _, conditionUncast := range conditions {
		condition := conditionUncast.(map[string]interface{})
		name, found, err := unstructured.NestedString(condition, "type")
		if !found || err != nil || strings.ToLower(name) != strings.ToLower(w.conditionName) {
			continue
		}
		status, found, err := unstructured.NestedString(condition, "status")
		if !found || err != nil {
			continue
		}
		return strings.ToLower(status) == strings.ToLower(w.conditionStatus), nil
	}

	return false, nil
}

func (w ConditionalWait) isConditionMet(event watch.Event) (bool, error) {
	if event.Type == watch.Deleted {
		return true, fmt.Errorf("object has been deleted before condition became true")
	}
	obj := event.Object.(*unstructured.Unstructured)
	return w.checkCondition(obj)
}
