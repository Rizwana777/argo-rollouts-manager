/*
Copyright 2023.

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

package rollouts

import (
	"context"

	rolloutsmanagerv1alpha1 "github.com/argoproj-labs/argo-rollouts-manager/api/v1alpha1"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// blank assignment to verify that RolloutManagerReconciler implements reconcile.Reconciler
var _ reconcile.Reconciler = &RolloutManagerReconciler{}

// RolloutManagerReconciler reconciles a RolloutManager object
type RolloutManagerReconciler struct {
	client.Client
	Scheme                       *runtime.Scheme
	OpenShiftRoutePluginLocation string

	// NamespaceScopedArgoRolloutsController is used to configure scope of Argo Rollouts controller
	// If value is true then deploy namespace-scoped Argo Rollouts controller else cluster-scoped
	NamespaceScopedArgoRolloutsController bool
}

var log = logr.Log.WithName("rollouts-controller")

const (
	serviceMonitorsCRDName = "servicemonitors.monitoring.coreos.com"
)

//+kubebuilder:rbac:groups=argoproj.io,resources=rolloutmanagers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=argoproj.io,resources=rolloutmanagers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=argoproj.io,resources=rolloutmanagers/finalizers,verbs=update
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps;endpoints;events;pods;namespaces;secrets;serviceaccounts;services;services/finalizers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=podtemplates;deployments;replicasets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=deployments,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
//+kubebuilder:rbac:groups="",resources=podtemplates,verbs=get;list;watch
//+kubebuilder:rbac:groups="appmesh.k8s.aws",resources=virtualnodes;virtualrouters,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups="appmesh.k8s.aws",resources=virtualservices,verbs=get;list;watch
//+kubebuilder:rbac:groups="argoproj.io",resources=analysisruns;analysisruns/finalizers;experiments;experiments/finalizers,verbs=create;get;list;watch;update;patch;delete;deletecollection
//+kubebuilder:rbac:groups="argoproj.io",resources=analysistemplates,verbs=create;get;list;watch;update;patch;delete;deletecollection
//+kubebuilder:rbac:groups="argoproj.io",resources=clusteranalysistemplates,verbs=create;get;list;watch;update;patch;delete;deletecollection
//+kubebuilder:rbac:groups="argoproj.io",resources=rollouts;rollouts/finalizers;rollouts/status;rollouts/scale,verbs=create;get;list;watch;update;patch;delete;deletecollection
//+kubebuilder:rbac:groups="batch",resources=jobs,verbs=create;get;list;watch;update;patch;delete
//+kubebuilder:rbac:groups="coordination.k8s.io",resources=leases,verbs=create;get;update
//+kubebuilder:rbac:groups="elbv2.k8s.aws",resources=targetgroupbindings,verbs=list;get
//+kubebuilder:rbac:groups="extensions",resources=ingresses,verbs=create;get;list;watch;patch
//+kubebuilder:rbac:groups="getambassador.io",resources=ambassadormappings;mappings,verbs=create;watch;get;update;list;delete
//+kubebuilder:rbac:groups="networking.istio.io",resources=destinationrules;virtualservices,verbs=watch;get;update;patch;list
//+kubebuilder:rbac:groups="networking.k8s.io",resources=ingresses,verbs=create;watch;get;update;patch;list
//+kubebuilder:rbac:groups="split.smi-spec.io",resources=trafficsplits,verbs=create;watch;get;update;patch
//+kubebuilder:rbac:groups="traefik.containo.us",resources=traefikservices,verbs=watch;get;update
//+kubebuilder:rbac:groups="x.getambassador.io",resources=ambassadormappings;mappings,verbs=create;watch;get;update;list;delete
//+kubebuilder:rbac:groups="apisix.apache.org",resources=apisixroutes,verbs=watch;get;update
//+kubebuilder:rbac:groups="route.openshift.io",resources=routes,verbs=create;watch;get;update;patch;list
//+kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=create;watch;get;update;patch;list

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *RolloutManagerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLogger := logr.FromContext(ctx, "Request.Namespace", req.Namespace, "Request.Name", req.Name)
	reqLogger.Info("Reconciling RolloutManager")

	// First retrieve the Namespace of the request: if it's being deleted, no more work for us.
	rolloutManagerNamespace := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: req.Namespace}}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&rolloutManagerNamespace), &rolloutManagerNamespace); err != nil {
		if apierrors.IsNotFound(err) { // If Namespace doesn't exist, our work is done
			reqLogger.Info("Skipping reconciliation of RolloutManager as request Namespace no longer exists")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err // Any other error, return it
	} else {
		// If the Namespace is in the process of being deleted, no more work required for us.
		if rolloutManagerNamespace.DeletionTimestamp != nil {
			return ctrl.Result{}, nil
		}
	}

	// Next, fetch and reconcile the RolloutManager instance
	rolloutManager := &rolloutsmanagerv1alpha1.RolloutManager{}
	if err := r.Client.Get(ctx, req.NamespacedName, rolloutManager); err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	res, reconcileErr := r.reconcileRolloutsManager(ctx, *rolloutManager)

	// Set the condition/phase on the RolloutManager status  (before we check the error from reconcileRolloutManager, below)
	if err := updateStatusConditionOfRolloutManager(ctx, res, rolloutManager, r.Client, log); err != nil {
		log.Error(err, "unable to update status of RolloutManager")
		return reconcile.Result{}, err
	}

	// Next return the reconcileErr if applicable
	if reconcileErr != nil {
		return reconcile.Result{}, reconcileErr
	}

	return reconcile.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RolloutManagerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	bld := ctrl.NewControllerManagedBy(mgr)
	// Watch for changes to primary resource RolloutManager.
	bld.For(&rolloutsmanagerv1alpha1.RolloutManager{})

	// Watch for changes to ConfigMap sub-resources owned by RolloutManager.
	bld.Owns(&corev1.ConfigMap{})

	// Watch for changes to Secret sub-resources owned by RolloutManager.
	bld.Owns(&corev1.Secret{})

	// Watch for changes to Service sub-resources owned by RolloutManager.
	bld.Owns(&corev1.Service{})

	// Watch for changes to Deployment sub-resources owned by RolloutManager.
	bld.Owns(&appsv1.Deployment{})

	// Watch for changes to Role sub-resources owned by RolloutManager.
	bld.Owns(&rbacv1.Role{})

	// Watch for changes to ClusterRole sub-resources owned by RolloutManager.
	bld.Owns(&rbacv1.ClusterRole{})

	// Watch for changes to RoleBinding sub-resources owned by RolloutManager.
	bld.Owns(&rbacv1.RoleBinding{})

	// Watch for changes to ClusterRoleBinding sub-resources owned by RolloutManager.
	bld.Owns(&rbacv1.ClusterRoleBinding{})

	if crdExists, err := r.doesCRDExist(mgr.GetConfig(), serviceMonitorsCRDName); err != nil {
		return err
	} else if crdExists {
		// We only attempt to own ServiceMonitor if it exists on the cluster on startup
		bld.Owns(&monitoringv1.ServiceMonitor{})
	}

	return bld.Complete(r)
}

// doesCRDExist checks if a CRD is present in the cluster, by using the discovery client.
//
// NOTE: this function should only be called from SetupWithManager. There are more efficient methods to determine this, elsewhere.
func (r *RolloutManagerReconciler) doesCRDExist(cfg *rest.Config, crdName string) (bool, error) {

	// Idealy we would use client.Client to retrieve the CRD, here, but since the manager has not yet started, we don't have access to the client from the manager. We would need to convert the rest.Config into a client.Client, and it's easier to use

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return false, err
	}
	apiResources, err := discoveryClient.ServerResourcesForGroupVersion("monitoring.coreos.com/v1")
	if err != nil {
		return false, err
	}
	for _, resource := range apiResources.APIResources {
		if resource.Name == crdName {
			return true, nil
		}
	}
	return false, nil
}
