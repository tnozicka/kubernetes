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

package rollout

import (
	"context"
	"fmt"
	"time"

	"github.com/golang/glog"
	"github.com/spf13/cobra"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"k8s.io/kubernetes/pkg/kubectl"
	"k8s.io/kubernetes/pkg/kubectl/cmd/templates"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions"
	"k8s.io/kubernetes/pkg/kubectl/genericclioptions/resource"
	"k8s.io/kubernetes/pkg/kubectl/polymorphichelpers"
	"k8s.io/kubernetes/pkg/kubectl/util/i18n"
	"k8s.io/kubernetes/pkg/util/interrupt"
)

var (
	status_long = templates.LongDesc(`
		Show the status of the rollout.

		By default 'rollout status' will watch the status of the latest rollout
		until it's done. If you don't want to wait for the rollout to finish then
		you can use --watch=false. Note that if a new rollout starts in-between, then
		'rollout status' will continue watching the latest revision. If you want to
		pin to a specific revision and abort if it is rolled over by another revision,
		use --revision=N where N is the revision you need to watch for.`)

	status_example = templates.Examples(`
		# Watch the rollout status of a deployment
		kubectl rollout status deployment/nginx`)
)

type RolloutStatusOptions struct {
	FilenameOptions *resource.FilenameOptions
	genericclioptions.IOStreams

	Namespace        string
	EnforceNamespace bool
	BuilderArgs      []string

	Watch    bool
	Revision int64
	Timeout  time.Duration

	StatusViewer func(*meta.RESTMapping) (kubectl.StatusViewer, error)

	Builder *resource.Builder
}

func NewRolloutStatusOptions(streams genericclioptions.IOStreams) *RolloutStatusOptions {
	return &RolloutStatusOptions{
		FilenameOptions: &resource.FilenameOptions{},
		IOStreams:       streams,
		Watch:           true,
		Timeout:         0,
	}
}

func NewCmdRolloutStatus(f cmdutil.Factory, streams genericclioptions.IOStreams) *cobra.Command {
	o := NewRolloutStatusOptions(streams)

	validArgs := []string{"deployment", "daemonset", "statefulset"}

	cmd := &cobra.Command{
		Use: "status (TYPE NAME | TYPE/NAME) [flags]",
		DisableFlagsInUseLine: true,
		Short:   i18n.T("Show the status of the rollout"),
		Long:    status_long,
		Example: status_example,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(o.Complete(f, args))
			cmdutil.CheckErr(o.Validate(cmd, args))
			cmdutil.CheckErr(o.Run(f))
		},
		ValidArgs: validArgs,
	}

	usage := "identifying the resource to get from a server."
	cmdutil.AddFilenameOptionFlags(cmd, o.FilenameOptions, usage)
	cmd.Flags().BoolVarP(&o.Watch, "watch", "w", o.Watch, "Watch the status of the rollout until it's done.")
	cmd.Flags().Int64Var(&o.Revision, "revision", o.Revision, "Pin to a specific revision for showing its status. Defaults to 0 (last revision).")
	cmd.Flags().DurationVar(&o.Timeout, "timeout", o.Timeout, "The length of time to wait before ending watch, zero means never. Any other values should contain a corresponding time unit (e.g. 1s, 2m, 3h).")

	return cmd
}

func (o *RolloutStatusOptions) Complete(f cmdutil.Factory, args []string) error {
	o.Builder = f.NewBuilder()

	var err error
	o.Namespace, o.EnforceNamespace, err = f.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}

	o.BuilderArgs = args
	o.StatusViewer = func(mapping *meta.RESTMapping) (kubectl.StatusViewer, error) {
		return polymorphichelpers.StatusViewerFn(f, mapping)
	}
	return nil
}

func (o *RolloutStatusOptions) Validate(cmd *cobra.Command, args []string) error {
	if len(args) == 0 && cmdutil.IsFilenameSliceEmpty(o.FilenameOptions.Filenames) {
		return cmdutil.UsageErrorf(cmd, "Required resource not specified.")
	}
	return nil
}

func (o *RolloutStatusOptions) Run(f cmdutil.Factory) error {
	r := o.Builder.
		WithScheme(legacyscheme.Scheme).
		NamespaceParam(o.Namespace).DefaultNamespace().
		FilenameParam(o.EnforceNamespace, o.FilenameOptions).
		ResourceTypeOrNameArgs(true, o.BuilderArgs...).
		SingleResourceType().
		Latest().
		Do()
	err := r.Err()
	if err != nil {
		return err
	}

	infos, err := r.Infos()
	if err != nil {
		return err
	}
	if len(infos) != 1 {
		return fmt.Errorf("rollout status is only supported on individual resources and resource collections - %d resources were found", len(infos))
	}
	info := infos[0]
	mapping := info.ResourceMapping()

	obj, err := r.Object()
	if err != nil {
		return err
	}

	restClient, err := f.ClientForMapping(mapping)
	if err != nil {
		return err
	}

	statusViewer, err := o.StatusViewer(mapping)
	if err != nil {
		return err
	}

	revision := o.Revision
	if revision < 0 {
		return fmt.Errorf("revision must be a positive integer: %v", revision)
	}

	lw := cache.NewListWatchFromClient(restClient, mapping.Resource.String(), info.Namespace, fields.OneTermEqualSelector("metadata.name", info.Name))

	preconditionFunc := func(store cache.Store) (bool, error) {
		_, exists, err := store.Get(&metav1.ObjectMeta{Namespace: info.Namespace, Name: info.Name})
		if err != nil {
			return true, err
		}
		if !exists {
			// We need to make sure we see the object in the cache before we start waiting for events
			// or we would be waiting for the timeout if such object didn't exist.
			return true, apierrors.NewNotFound(mapping.Resource.GroupResource(), info.Name)
		}

		return false, nil
	}

	// if the rollout isn't done yet, keep watching deployment status
	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	intr := interrupt.New(nil, cancel)
	return intr.Run(func() error {
		_, err = watchtools.UntilWithInformer(ctx, lw, obj, 0, preconditionFunc, func(e watch.Event) (bool, error) {
			switch t := e.Type; t {
			case watch.Added, watch.Modified:
				status, done, err := statusViewer.Status(e.Object, revision)
				if err != nil {
					return false, err
				}
				fmt.Fprintf(o.Out, "%s", status)
				// Quit waiting if the rollout is done
				if done {
					return true, nil
				}

				shouldWatch := o.Watch
				if !shouldWatch {
					return true, nil
				}

				return false, nil

			case watch.Deleted:
				// We need to abort to avoid cases of recreation and not to silently watch the wrong (new) object
				return true, fmt.Errorf("object has been deleted")

			case watch.Error:
				return true, fmt.Errorf("watch failed: %v", e.Object)

			default:
				glog.Fatalf("unexpected event %#v", e)
				panic("glog failed to exit")
			}
		})
		return err
	})
}
