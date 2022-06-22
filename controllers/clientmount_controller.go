/*
 * Copyright 2021, 2022 Hewlett Packard Enterprise Development LP
 * Other additional copyright holders may be indicated within.
 *
 * The entirety of this work is licensed under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 *
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package controllers

import (
	"context"
	"os"
	"reflect"
	"strings"

	"github.com/go-logr/logr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	dwsv1alpha1 "github.com/HewlettPackard/dws/api/v1alpha1"
)

// ClientMountReconciler reconciles a ClientMount object
type ClientMountReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

const (
	// finalizerClientMount defines the key used for the finalizer
	finalizerClientMount = "dws.cray.hpe.com/client_mount"
)

//+kubebuilder:rbac:groups=dws.cray.hpe.com,resources=clientmounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dws.cray.hpe.com,resources=clientmounts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dws.cray.hpe.com,resources=clientmounts/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ClientMountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	clientMount := &dwsv1alpha1.ClientMount{}
	if err := r.Get(ctx, req.NamespacedName, clientMount); err != nil {
		// ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Create a status updater that handles the call to status().Update() if any of the fields
	// in clientMount.Status change
	statusUpdater := newClientMountStatusUpdater(clientMount)
	defer func() {
		if err == nil {
			err = statusUpdater.close(ctx, r)
		}
	}()

	// Handle cleanup if the resource is being deleted
	if !clientMount.GetDeletionTimestamp().IsZero() {
		if !controllerutil.ContainsFinalizer(clientMount, finalizerClientMount) {
			return ctrl.Result{}, nil
		}

		controllerutil.RemoveFinalizer(clientMount, finalizerClientMount)
		if err := r.Update(ctx, clientMount); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// Create the status section if it doesn't exist yet
	if len(clientMount.Status.Mounts) != len(clientMount.Spec.Mounts) {
		clientMount.Status.Mounts = make([]dwsv1alpha1.ClientMountInfoStatus, len(clientMount.Spec.Mounts))
	}

	// Initialize the status section if the desired state doesn't match the status state
	if clientMount.Status.Mounts[0].State != clientMount.Spec.DesiredState {
		for i := 0; i < len(clientMount.Status.Mounts); i++ {
			clientMount.Status.Mounts[i].State = clientMount.Spec.DesiredState
			clientMount.Status.Mounts[i].Ready = false
			clientMount.Status.Mounts[i].Message = ""
		}

		return ctrl.Result{}, nil
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(clientMount, finalizerClientMount) {
		controllerutil.AddFinalizer(clientMount, finalizerClientMount)
		if err := r.Update(ctx, clientMount); err != nil {
			return ctrl.Result{Requeue: true}, nil
		}

		return ctrl.Result{}, nil
	}

	for i, _ := range clientMount.Spec.Mounts {
		clientMount.Status.Mounts[i].Message = ""
		clientMount.Status.Mounts[i].Ready = true
	}

	return ctrl.Result{}, nil
}

type clientMountStatusUpdater struct {
	clientMount    *dwsv1alpha1.ClientMount
	existingStatus dwsv1alpha1.ClientMountStatus
}

func newClientMountStatusUpdater(c *dwsv1alpha1.ClientMount) *clientMountStatusUpdater {
	return &clientMountStatusUpdater{
		clientMount:    c,
		existingStatus: (*c.DeepCopy()).Status,
	}
}

func (c *clientMountStatusUpdater) close(ctx context.Context, r *ClientMountReconciler) error {
	if !reflect.DeepEqual(c.clientMount.Status, c.existingStatus) {
		err := r.Status().Update(ctx, c.clientMount)
		if !apierrors.IsConflict(err) {
			return err
		}
	}

	return nil
}

func filterByNonRabbitNamespacePrefixForTest() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(object client.Object) bool {
		return !strings.HasPrefix(object.GetNamespace(), "rabbit")
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClientMountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&dwsv1alpha1.ClientMount{})

	if _, found := os.LookupEnv("NNF_TEST_ENVIRONMENT"); found {
		builder = builder.WithEventFilter(filterByNonRabbitNamespacePrefixForTest())
	}

	return builder.Complete(r)
}