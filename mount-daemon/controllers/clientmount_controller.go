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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	dwsv1alpha1 "github.com/HewlettPackard/dws/api/v1alpha1"
	"github.com/HewlettPackard/dws/utils/updater"
)

// ClientMountReconciler reconciles a ClientMount object
type ClientMountReconciler struct {
	client.Client
	Mock   bool
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
	log := r.Log.WithValues("ClientMount", req.NamespacedName)
	clientMount := &dwsv1alpha1.ClientMount{}
	if err := r.Get(ctx, req.NamespacedName, clientMount); err != nil {
		// ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Create a status updater that handles the call to r.Status().Update() if any of the fields
	// in clientMount.Status{} change
	statusUpdater := updater.NewStatusUpdater[*dwsv1alpha1.ClientMountStatus](clientMount)
	defer func() { err = statusUpdater.CloseWithStatusUpdate(ctx, r, err) }()

	// Handle cleanup if the resource is being deleted
	if !clientMount.GetDeletionTimestamp().IsZero() {
		if !controllerutil.ContainsFinalizer(clientMount, finalizerClientMount) {
			return ctrl.Result{}, nil
		}

		// Unmount everything before removing the finalizer
		log.Info("Unmounting all file systems due to resource deletion")
		if err := r.unmountAll(ctx, clientMount); err != nil {
			return ctrl.Result{}, err
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

	clientMount.Status.Error = nil

	if clientMount.Spec.DesiredState == dwsv1alpha1.ClientMountStateMounted {
		err := r.mountAll(ctx, clientMount)
		if err != nil {
			resourceError := dwsv1alpha1.NewResourceError("Mount failed", err)
			log.Info(resourceError.Error())

			clientMount.Status.Error = resourceError
			return ctrl.Result{RequeueAfter: time.Second * time.Duration(10)}, nil
		}
	} else if clientMount.Spec.DesiredState == dwsv1alpha1.ClientMountStateUnmounted {
		err := r.unmountAll(ctx, clientMount)
		if err != nil {
			resourceError := dwsv1alpha1.NewResourceError("Unmount failed", err)
			log.Info(resourceError.Error())

			clientMount.Status.Error = resourceError
			return ctrl.Result{RequeueAfter: time.Second * time.Duration(10)}, nil
		}
	}

	return ctrl.Result{}, nil
}

// unmountAll unmounts all the file systems listed in the spec.Mounts list
func (r *ClientMountReconciler) unmountAll(ctx context.Context, clientMount *dwsv1alpha1.ClientMount) error {
	log := r.Log.WithValues("ClientMount", types.NamespacedName{Name: clientMount.Name, Namespace: clientMount.Namespace})

	var firstError error = nil
	for i, mount := range clientMount.Spec.Mounts {
		err := r.unmount(ctx, mount, log)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			clientMount.Status.Mounts[i].Ready = false
		} else {
			clientMount.Status.Mounts[i].Ready = true
		}
	}

	return firstError
}

// unmount unmounts a single mount point described in the ClientMountInfo object
func (r *ClientMountReconciler) unmount(ctx context.Context, clientMountInfo dwsv1alpha1.ClientMountInfo, log logr.Logger) error {
	state, err := r.checkMount(clientMountInfo.MountPath)
	if err != nil {
		return err
	}

	if state == dwsv1alpha1.ClientMountStateMounted {

		output, err := r.run("umount " + clientMountInfo.MountPath)
		if err != nil {
			log.Info("Could not unmount file system", "mount path", clientMountInfo.MountPath, "Error output", output)
			return err
		}
	}

	if clientMountInfo.Device.Type == dwsv1alpha1.ClientMountDeviceTypeLVM {
		if err := r.configureLVMDevice(clientMountInfo.Device.LVM, false, clientMountInfo.Type == "gfs2"); err != nil {
			log.Error(err, "Could not deactivate LVM volume", "mount path", clientMountInfo.MountPath)
			return err
		}
	}

	// Remove the mount directory. It's not a big deal if this fails, so we just log a failure and don't return it
	if err := r.rmdir(clientMountInfo.MountPath); err != nil {
		log.Error(err, "Unable to remove mount directory", "Path", clientMountInfo.MountPath)
	}

	log.Info("Unmounted file system", "mount path", clientMountInfo.MountPath)
	return nil
}

// mountAll mounts all the file systems listed in the spec.Mounts list
func (r *ClientMountReconciler) mountAll(ctx context.Context, clientMount *dwsv1alpha1.ClientMount) error {
	log := r.Log.WithValues("ClientMount", types.NamespacedName{Name: clientMount.Name, Namespace: clientMount.Namespace})

	var firstError error = nil
	for i, mount := range clientMount.Spec.Mounts {
		err := r.mount(ctx, mount, log)
		if err != nil {
			if firstError == nil {
				firstError = err
			}
			clientMount.Status.Mounts[i].Ready = false
		} else {
			clientMount.Status.Mounts[i].Ready = true
		}
	}

	return firstError
}

// mount mounts a single mount point described in the ClientMountInfo object
func (r *ClientMountReconciler) mount(ctx context.Context, clientMountInfo dwsv1alpha1.ClientMountInfo, log logr.Logger) error {

	// Check whether the file system is already mounted
	state, err := r.checkMount(clientMountInfo.MountPath)
	if err != nil {
		return err
	}

	if state == dwsv1alpha1.ClientMountStateMounted {
		log.Info("Already mounted")
		return nil
	}

	device, err := r.getDevice(clientMountInfo)
	if err != nil {
		return err
	}

	// Create the mount file or directory
	switch clientMountInfo.TargetType {
	case "directory":
		if err := r.mkdir(clientMountInfo.MountPath); err != nil {
			log.Error(err, "Could not create mount directory", "mount path", clientMountInfo.MountPath, "device", device)
			return err
		}
	case "file":
		// Create the parent directory and then the file
		if err := r.mkdir(filepath.Dir(clientMountInfo.MountPath)); err != nil {
			log.Error(err, "Could not create mount parent directory", "mount path", clientMountInfo.MountPath, "device", device)
			return err
		}

		if err := r.createFile(clientMountInfo.MountPath); err != nil {
			log.Error(err, "Could not create mount file", "mount path", clientMountInfo.MountPath, "device", device)
			return err
		}
	}

	// Run the mount command
	mountCmd := "mount -t " + clientMountInfo.Type + " " + device + " " + clientMountInfo.MountPath
	if clientMountInfo.Options != "" {
		mountCmd = mountCmd + " -o " + clientMountInfo.Options
	}

	output, err := r.run(mountCmd)
	if err != nil {
		log.Info("Could not mount file system", "mount path", clientMountInfo.MountPath, "device", device, "Error output", output)
		return err
	}

	log.Info("Mounted file system", "Mount path", clientMountInfo.MountPath, "device", device)

	return nil
}

// getDevice builds the device string for the mount command. This is dependent on the type of file
func (r *ClientMountReconciler) getDevice(clientMountInfo dwsv1alpha1.ClientMountInfo) (string, error) {
	switch clientMountInfo.Device.Type {
	case dwsv1alpha1.ClientMountDeviceTypeLustre:
		device := clientMountInfo.Device.Lustre.MgsAddresses + ":/" + clientMountInfo.Device.Lustre.FileSystemName

		return device, nil
	case dwsv1alpha1.ClientMountDeviceTypeLVM:
		if err := r.configureLVMDevice(clientMountInfo.Device.LVM, true, clientMountInfo.Type == "gfs2"); err != nil {
			return "", err
		}

		return filepath.Join("/dev", clientMountInfo.Device.LVM.VolumeGroup, clientMountInfo.Device.LVM.LogicalVolume), nil
	}

	return "", fmt.Errorf("Invalid device type")
}

// configureLVMDevice will configure the provided LVM device with the desired activate/deactivate option
func (r *ClientMountReconciler) configureLVMDevice(lvm *dwsv1alpha1.ClientMountDeviceLVM, activate bool, shared bool) error {
	output, err := r.run(fmt.Sprintf("lvs --noheadings --separator ' '"))
	if err != nil {
		return err
	}

	if r.Mock {
		return nil
	}

	// Parse the lvs output. Example with headings:
	// [root@rabbit-compute-2 mattr]# lvs
	// LV                          VG                          Attr       LSize
	//  default-mattr2-0-xfs-0-1_lv default-mattr2-0-xfs-0-1_vg -wi-------  46.59g
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		if fields[0] != lvm.LogicalVolume {
			continue
		}

		if fields[1] != lvm.VolumeGroup {
			continue
		}

		// Check the 5th letter of the attributes map to see if the LV is activated
		isActive := string(fields[2][4]) == "a"
		if activate && !isActive {

			sharedOption := ""
			// Start lock if needed
			if shared {
				output, err := r.run(fmt.Sprintf("vgchange --lockstart %s", lvm.VolumeGroup))
				if err != nil {
					return dwsv1alpha1.NewResourceError(output, err).WithUserMessage("Client could not access storage").WithFatal()
				}

				sharedOption = "s" // activate with shared option
			}

			// Activate the LV if needed
			output, err := r.run(fmt.Sprintf("vgchange --activate %sy %s", sharedOption, lvm.VolumeGroup))
			if err != nil {
				return dwsv1alpha1.NewResourceError(output, err).WithUserMessage("Client could not access storage").WithFatal()
			}

		} else if !activate && isActive {
			output, err := r.run(fmt.Sprintf("vgchange --activate n %s", lvm.VolumeGroup))
			if err != nil {
				return dwsv1alpha1.NewResourceError(output, err).WithUserMessage("Client could not release storage").WithFatal()
			}

			if shared {
				output, err := r.run(fmt.Sprintf("vgchange --lockstop %s", lvm.VolumeGroup))
				if err != nil {
					return dwsv1alpha1.NewResourceError(output, err).WithUserMessage("Client could not release storage").WithFatal()
				}
			}
		}

		return nil
	}

	err = dwsv1alpha1.NewResourceError(fmt.Sprintf("Could not find VG/LV pair %s/%s", lvm.VolumeGroup, lvm.LogicalVolume)+": "+output, nil).WithFatal()
	r.Log.Info(err.Error())

	return err
}

// checkMount checks whether a file system is mounted at the path specified in "mountPath"
func (r *ClientMountReconciler) checkMount(mountPath string) (dwsv1alpha1.ClientMountState, error) {
	output, err := r.run("mount")
	if err != nil {
		return dwsv1alpha1.ClientMountStateUnmounted, dwsv1alpha1.NewResourceError(output, err)
	}

	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			if fields[2] == mountPath {
				return dwsv1alpha1.ClientMountStateMounted, nil
			}
		}
	}

	return dwsv1alpha1.ClientMountStateUnmounted, nil
}

func (r *ClientMountReconciler) createFile(path string) error {
	if r.Mock {
		r.Log.Info("Touch file", "Path", path)
		return nil
	}

	return os.WriteFile(path, []byte(""), 0644)
}

func (r *ClientMountReconciler) rmdir(path string) error {
	if r.Mock {
		r.Log.Info("rmdir", "Path", path)
		return nil
	}

	return os.Remove(path)
}

func (r *ClientMountReconciler) mkdir(path string) error {
	if r.Mock {
		r.Log.Info("Mkdir", "Path", path)
		return nil
	}

	return os.MkdirAll(path, 0755)
}

// run runs a command on the host OS and returns the output as a string.
func (r *ClientMountReconciler) run(c string) (string, error) {
	if r.Mock {
		r.Log.Info("Run", "Command", c)
		return "", nil
	}

	output, err := exec.Command("bash", "-c", c).Output()

	return string(output), err
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
