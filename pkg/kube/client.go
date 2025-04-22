// Copyright 2024 kharf
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kube

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// InMemoryRESTClientGetter is a Helm RESTClientGetter implementatiion, which caches
// discovery information in memory and will stay up-to-date if Invalidate is
// called with regularity.
//
// NOTE: The client will NOT resort to live lookups on cache misses.
type InMemoryRESTClientGetter struct {
	Cfg        *rest.Config
	RestMapper meta.RESTMapper
}

var _ action.RESTClientGetter = (*InMemoryRESTClientGetter)(nil)

func (c *InMemoryRESTClientGetter) ToRESTConfig() (*rest.Config, error) {
	return c.Cfg, nil
}

func (c *InMemoryRESTClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	client, err := discovery.NewDiscoveryClientForConfig(c.Cfg)
	return memory.NewMemCacheClient(client), err
}

func (c *InMemoryRESTClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	return c.RestMapper, nil
}

func (c *InMemoryRESTClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
}

type applyOptions struct {
	dryRun bool
	force  bool
}

// ApplyOption is a specific configuration used for applying changes to an object.
type ApplyOption func(*applyOptions)

// DryRun indicates that modifications should not be persisted.
func DryRunApply(value bool) ApplyOption {
	return func(opts *applyOptions) {
		opts.dryRun = value
	}
}

// Force indicates that conflicts should not error.
func ForceApply(value bool) ApplyOption {
	return func(opts *applyOptions) {
		opts.force = value
	}
}

type patchOptions struct {
	patchType types.PatchType
}

// PatchOption is a specific configuration used for applying json patch changes to an object.
type PatchOption func(*patchOptions)

func PatchType(value types.PatchType) PatchOption {
	return func(opts *patchOptions) {
		opts.patchType = value
	}
}

// Client connects to a Kubernetes cluster
// to create, read, update and delete manifests/objects.
type Client[T any, R any] interface {
	// Apply applies changes to an object through a Server-Side Apply
	// and takes the ownership of this object.
	// The object is created when it does not exist.
	// It errors on conflicts if force is set to false.
	Apply(ctx context.Context, obj *T, fieldManager string, opts ...ApplyOption) (*R, error)
	// Patch applies partial changes to an object and takes ownership of this object/field.
	Patch(ctx context.Context, obj *T, fieldManager string, opts ...PatchOption) (*R, error)
	// Get retrieves the unstructured object from a Kubernetes cluster.
	Get(ctx context.Context, obj *T) (*R, error)
	// Delete removes the object from the Kubernetes cluster.
	Delete(ctx context.Context, obj *T) error
	// Returns the [meta.RESTMapper] associated with this client.
	RESTMapper() meta.RESTMapper
}

// DynamicClient connects to a Kubernetes cluster
// to create, read, update and delete unstructured manifests/objects.
type DynamicClient struct {
	dynamicClient *dynamic.DynamicClient
	restMapper    meta.RESTMapper
}

var _ Client[unstructured.Unstructured, unstructured.Unstructured] = (*DynamicClient)(nil)

// NewDynamicClient constructs a new DynamicClient,
// which connects to a Kubernetes cluster to create, read, update and delete unstructured manifests/objects.
func NewDynamicClient(config *rest.Config) (*DynamicClient, error) {
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, err
	}

	restMapper, err := apiutil.NewDynamicRESTMapper(config, httpClient)
	if err != nil {
		return nil, err
	}

	config = dynamic.ConfigFor(config)
	// for serializing the options
	config.GroupVersion = &schema.GroupVersion{}

	restClient, err := rest.RESTClientForConfigAndClient(config, httpClient)
	if err != nil {
		return nil, err
	}

	dynClient := dynamic.New(restClient)

	return &DynamicClient{
		dynamicClient: dynClient,
		restMapper:    restMapper,
	}, nil
}

// Apply applies changes to an object through a Server-Side Apply
// and takes the ownership of this object.
// The object is created when it does not exist.
// It errors on conflicts if force is set to false.
func (client *DynamicClient) Apply(
	ctx context.Context,
	obj *unstructured.Unstructured,
	fieldManager string,
	opts ...ApplyOption,
) (*unstructured.Unstructured, error) {
	applyOptions := new(applyOptions)
	for _, opt := range opts {
		opt(applyOptions)
	}

	return client.apply(
		ctx,
		obj,
		fieldManager,
		applyOptions,
	)
}

func (client *DynamicClient) apply(
	ctx context.Context,
	obj *unstructured.Unstructured,
	fieldManager string,
	options *applyOptions,
) (*unstructured.Unstructured, error) {
	resourceInterface, err := client.resourceInterface(obj.GroupVersionKind(), obj.GetNamespace())
	if err != nil {
		return nil, err
	}

	applyOptions := v1.ApplyOptions{
		FieldManager: fieldManager,
		Force:        options.force,
	}

	if options.dryRun {
		applyOptions.DryRun = []string{"All"}
	}

	runtimeObj, err := resourceInterface.Apply(ctx, obj.GetName(), obj, applyOptions)
	if err != nil {
		return nil, err
	}

	if !options.dryRun {
		timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		_, err = client.wait(
			timeoutCtx,
			obj.GetName(),
			v1.TypeMeta{
				Kind:       obj.GetKind(),
				APIVersion: obj.GetAPIVersion(),
			},
			resourceInterface,
		)

		if err != nil {
			return nil, err
		}
	}

	return runtimeObj, nil
}

// Patch applies partial changes to an object and takes ownership of this object/field.
func (client *DynamicClient) Patch(
	ctx context.Context,
	obj *unstructured.Unstructured,
	fieldManager string,
	opts ...PatchOption,
) (*unstructured.Unstructured, error) {
	options := new(patchOptions)
	for _, opt := range opts {
		opt(options)
	}

	return client.patch(ctx, obj, fieldManager, options)
}

func (client *DynamicClient) patch(
	ctx context.Context,
	obj *unstructured.Unstructured,
	fieldManager string,
	options *patchOptions,
) (*unstructured.Unstructured, error) {
	resourceInterface, err := client.resourceInterface(obj.GroupVersionKind(), obj.GetNamespace())
	if err != nil {
		return nil, err
	}

	patchOptions := v1.PatchOptions{
		FieldManager: fieldManager,
	}

	objBytes, err := runtime.Encode(unstructured.UnstructuredJSONScheme, obj)
	if err != nil {
		return nil, err
	}

	runtimeObj, err := resourceInterface.Patch(
		ctx,
		obj.GetName(),
		options.patchType,
		objBytes,
		patchOptions,
	)
	if err != nil {
		return nil, err
	}

	return runtimeObj, nil
}

func (client *DynamicClient) wait(
	ctx context.Context,
	name string,
	typeMeta v1.TypeMeta,
	resourceInterface dynamic.ResourceInterface,
) (bool, error) {
	if typeMeta.Kind != "CustomResourceDefinition" {
		return true, nil
	}

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	obj, err := resourceInterface.Get(ctx, name, v1.GetOptions{
		TypeMeta: typeMeta,
	})
	if err != nil {
		return false, err
	}

	conditions := getConditions(obj)
	ok := slices.ContainsFunc(conditions, func(cond condition) bool {
		return cond.cType == string(apiextensionsv1.Established) &&
			cond.status == string(apiextensionsv1.ConditionTrue)
	})
	if ok {
		return true, nil
	}

	time.Sleep(1 * time.Second)
	return client.wait(ctx, name, typeMeta, resourceInterface)
}

type condition struct {
	cType  string
	status string
}

func getConditions(obj *unstructured.Unstructured) []condition {
	conditions := make([]condition, 0, 2)
	status, ok := obj.Object["status"]
	if !ok {
		return conditions
	}

	statusMap, ok := status.(map[string]interface{})
	if !ok {
		return conditions
	}

	conditionsArr, ok := statusMap["conditions"].([]interface{})
	if !ok {
		return conditions
	}

	for _, c := range conditionsArr {
		cond, ok := c.(map[string]interface{})
		if !ok {
			return conditions
		}

		t, ok := cond["type"].(string)
		if !ok {
			return conditions
		}

		status, ok := cond["status"].(string)
		if !ok {
			return conditions
		}

		conditions = append(conditions, condition{cType: t, status: status})
	}

	return conditions
}

// Delete removes the unstructured object from a Kubernetes cluster.
// Following fields have to be set on obj:
// - GVK, Namespace, Name
func (client *DynamicClient) Delete(ctx context.Context, obj *unstructured.Unstructured) error {
	resourceInterface, err := client.resourceInterface(obj.GroupVersionKind(), obj.GetNamespace())
	if err != nil {
		return err
	}
	if err := resourceInterface.Delete(ctx, obj.GetName(), v1.DeleteOptions{
		TypeMeta: v1.TypeMeta{
			Kind:       obj.GetKind(),
			APIVersion: obj.GetAPIVersion(),
		},
	}); err != nil {
		return err
	}
	return nil
}

var (
	ErrManifestNoMetadata = errors.New("Helm chart manifest has no metadata")
)

// Get retrieves the unstructured object from a Kubernetes cluster.
// Following fields have to be set on obj:
// - GVK, Name
func (client *DynamicClient) Get(
	ctx context.Context,
	obj *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	namespace := ""
	metadata, ok := obj.Object["metadata"].(map[string]interface{})
	if !ok {
		return nil, ErrManifestNoMetadata
	}

	namespace, ok = metadata["namespace"].(string)
	if !ok {
		namespace = ""
	}

	resourceInterface, err := client.resourceInterface(obj.GroupVersionKind(), namespace)
	if err != nil {
		return nil, err
	}

	name := metadata["name"].(string)
	foundObj, err := resourceInterface.Get(ctx, name, v1.GetOptions{
		TypeMeta: v1.TypeMeta{
			Kind:       obj.GetKind(),
			APIVersion: obj.GetAPIVersion(),
		},
	})

	if err != nil {
		return nil, err
	}

	return foundObj, nil
}

func (client *DynamicClient) RESTMapper() meta.RESTMapper {
	return client.restMapper
}

func (client *DynamicClient) resourceInterface(
	gvk schema.GroupVersionKind,
	namespace string,
) (dynamic.ResourceInterface, error) {
	restMapper := client.restMapper
	dynamicClient := client.dynamicClient
	mapping, err := restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return dynamicClient.Resource(mapping.Resource).Namespace(namespace), nil
	}
	return dynamicClient.Resource(mapping.Resource), nil
}

// DynamicClient connects to a Kubernetes cluster
// to create, read, update and delete extended unstructured manifests/objects.
type ExtendedDynamicClient struct {
	dynamicClient *DynamicClient
}

var _ Client[ExtendedUnstructured, unstructured.Unstructured] = (*ExtendedDynamicClient)(nil)

// NewExtendedDynamicClient constructs a new DynamicClient,
// which connects to a Kubernetes cluster to create, read, update and delete unstructured manifests/objects.
func NewExtendedDynamicClient(config *rest.Config) (*ExtendedDynamicClient, error) {
	dynClient, err := NewDynamicClient(config)
	if err != nil {
		return nil, err
	}

	return &ExtendedDynamicClient{
		dynamicClient: dynClient,
	}, nil
}

func (e *ExtendedDynamicClient) DynamicClient() *DynamicClient {
	return e.dynamicClient
}

func (e *ExtendedDynamicClient) Apply(
	ctx context.Context,
	obj *ExtendedUnstructured,
	fieldManager string,
	opts ...ApplyOption,
) (*unstructured.Unstructured, error) {
	applyOptions := new(applyOptions)
	for _, opt := range opts {
		opt(applyOptions)
	}

	runtimeObj, err := e.dynamicClient.Get(ctx, obj.Unstructured)
	if err != nil {
		statusErr, ok := err.(*k8sErrors.StatusError)
		if !ok || statusErr.Status().Reason != v1.StatusReasonNotFound {
			return nil, err
		}

		return e.dynamicClient.apply(
			ctx,
			obj.Unstructured,
			fieldManager,
			applyOptions,
		)
	}

	// https://github.com/kubernetes-sigs/structured-merge-diff
	managedFieldUpdate := &unstructured.Unstructured{}
	managedFieldUpdate.SetName(obj.GetName())
	managedFieldUpdate.SetNamespace(obj.GetNamespace())
	managedFieldUpdate.SetAPIVersion(obj.GetAPIVersion())
	managedFieldUpdate.SetKind(obj.GetKind())
	managedFields, err := cleanManagedFields(
		runtimeObj,
		fieldManager,
	)
	if err != nil {
		return nil, err
	}
	managedFieldUpdate.SetManagedFields(managedFields)

	if _, err := e.dynamicClient.patch(ctx, managedFieldUpdate, fieldManager, &patchOptions{
		patchType: types.MergePatchType,
	}); err != nil {
		return nil, err
	}

	// if there are no ignored fields, try to apply and return immediately.
	if obj.Metadata == nil {
		return e.dynamicClient.apply(
			ctx,
			obj.Unstructured,
			fieldManager,
			applyOptions,
		)
	}

	// First try applies without force and errors on conflict.
	// That is done to avoid ownership push around because there might be other managers specifically managing fields of manifests.
	// For example, HPAs managing replicas fields.
	originalForce := applyOptions.force
	applyOptions.force = false

	runtimeObj, err = e.dynamicClient.apply(
		ctx,
		obj.Unstructured,
		fieldManager,
		applyOptions,
	)

	if err != nil {
		statusErr, ok := err.(*k8sErrors.StatusError)
		if !ok || statusErr.Status().Reason != v1.StatusReasonConflict {
			return nil, err
		}

		// Retry with original force option.
		applyOptions.force = originalForce
		return e.applyWithoutIgnoredFields(
			ctx,
			obj,
			fieldManager,
			statusErr.Status().Details.Causes,
			applyOptions,
		)
	}

	return runtimeObj, nil
}

func (e *ExtendedDynamicClient) applyWithoutIgnoredFields(
	ctx context.Context,
	obj *ExtendedUnstructured,
	fieldManager string,
	conflictCauses []v1.StatusCause,
	applyOptions *applyOptions,
) (*unstructured.Unstructured, error) {
	unstr := obj.Unstructured
	if obj.Metadata != nil {
		unstr = obj.DeepCopy()

		for _, cause := range conflictCauses {
			if err := removeIgnoredFields(cause.Field, unstr.Object, *obj.Metadata); err != nil {
				return nil, err
			}
		}
	}

	return e.dynamicClient.apply(ctx, unstr, fieldManager, applyOptions)
}

var imposterFieldManagers = []string{
	"kubectl", "k9s",
}

func cleanManagedFields(
	runtimeObj *unstructured.Unstructured,
	fieldManager string,
) ([]v1.ManagedFieldsEntry, error) {
	var controllerManagedFieldsEntry v1.ManagedFieldsEntry
	manuallyManagedFields := make([]v1.ManagedFieldsEntry, 0)
	otherManagedFields := make([]v1.ManagedFieldsEntry, 0)
	for _, managedField := range runtimeObj.GetManagedFields() {
		if isImposter(managedField.Manager) && managedField.Subresource == "" {
			manuallyManagedFields = append(manuallyManagedFields, managedField)
			continue
		}

		if managedField.Manager == fieldManager && managedField.Subresource == "" {
			controllerManagedFieldsEntry = managedField
			continue
		}

		otherManagedFields = append(otherManagedFields, managedField)
	}

	for _, manuallyManagedField := range manuallyManagedFields {
		manualSet := fieldpath.Set{}
		err := manualSet.FromJSON(bytes.NewReader(manuallyManagedField.FieldsV1.Raw))
		if err != nil {
			return nil, err
		}

		controllerSet := fieldpath.Set{}
		if err := controllerSet.FromJSON(bytes.NewReader(controllerManagedFieldsEntry.FieldsV1.Raw)); err != nil {
			return nil, err
		}

		mergedSet := manualSet.Union(&controllerSet)

		controllerManagedFieldsEntry.FieldsV1.Raw, err = mergedSet.ToJSON()
		if err != nil {
			return nil, err
		}
	}

	otherManagedFields = append(otherManagedFields, controllerManagedFieldsEntry)
	return otherManagedFields, nil
}

func isImposter(
	manager string,
) bool {
	for _, imposter := range imposterFieldManagers {
		if strings.Contains(manager, imposter) {
			return true
		}
	}
	return false
}

// Patch applies partial changes to an object and takes ownership of this object/field.
func (client *ExtendedDynamicClient) Patch(
	ctx context.Context,
	obj *ExtendedUnstructured,
	fieldManager string,
	opts ...PatchOption,
) (*unstructured.Unstructured, error) {
	options := new(patchOptions)
	for _, opt := range opts {
		opt(options)
	}

	return client.dynamicClient.patch(ctx, obj.Unstructured, fieldManager, options)
}

func removeIgnoredFields(
	jsonPath string,
	unstrMap map[string]any,
	metadata ManifestMetadata,
) error {
	keys := strings.Split(jsonPath, ".")

	for i, key := range keys {
		if i == 0 {
			continue
		}

		if i == len(keys)-1 {
			fieldMetadata, ok := metadata.Node[key]
			if ok && fieldMetadata.Field != nil {
				if ok && fieldMetadata.Field.IgnoreInstr == OnConflict {
					delete(unstrMap, key)
				}
			}

			return nil
		}

		var found bool
		metadata, found = metadata.Node[key]
		if !found {
			break
		}
		unstrMap = unstrMap[key].(map[string]any)
	}

	return nil
}

func (e *ExtendedDynamicClient) Delete(ctx context.Context, obj *ExtendedUnstructured) error {
	return e.dynamicClient.Delete(ctx, obj.Unstructured)
}

func (e *ExtendedDynamicClient) Get(
	ctx context.Context,
	obj *ExtendedUnstructured,
) (*unstructured.Unstructured, error) {
	return e.dynamicClient.Get(ctx, obj.Unstructured)
}

func (e *ExtendedDynamicClient) RESTMapper() meta.RESTMapper {
	return e.dynamicClient.RESTMapper()
}
