// Package autopilot provides utility functions to config Autopilot as part of the stack
// which makes monitoring and diagnostics distributed compute infrastructure in the cloud easy and intuitive for Data Scientists and Administrators
// +groupName=datasciencecluster.opendatahub.io
package autopilot

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/go-logr/logr"
	operatorv1 "github.com/openshift/api/operator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dsciv1 "github.com/opendatahub-io/opendatahub-operator/v2/apis/dscinitialization/v1"
	"github.com/opendatahub-io/opendatahub-operator/v2/components"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/cluster"
	"github.com/opendatahub-io/opendatahub-operator/v2/pkg/deploy"
)

var (
	ComponentName = "autopilot"
	AutopilotPath = deploy.DefaultManifestPath + "/" + ComponentName
)

// Verifies that Autopilot implements ComponentInterface.
var _ components.ComponentInterface = (*Autopilot)(nil)

// Autopilot struct holds the configuration for the Autopilot component.
// +kubebuilder:object:generate=true
type Autopilot struct {
	components.Component `json:""`
}

func (c *Autopilot) OverrideManifests(ctx context.Context, _ cluster.Platform) error {
	// If devflags are set, update default manifests path
	if len(c.DevFlags.Manifests) != 0 {
		manifestConfig := c.DevFlags.Manifests[0]
		if err := deploy.DownloadManifests(ctx, ComponentName, manifestConfig); err != nil {
			return err
		}
		// If overlay is defined, update paths
		defaultKustomizePath := ""
		if manifestConfig.SourcePath != "" {
			defaultKustomizePath = manifestConfig.SourcePath
		}
		AutopilotPath = filepath.Join(deploy.DefaultManifestPath, ComponentName, defaultKustomizePath)
	}

	return nil
}

func (c *Autopilot) GetComponentName() string {
	return ComponentName
}

func (c *Autopilot) ReconcileComponent(ctx context.Context, cli client.Client, logger logr.Logger,
	owner metav1.Object, dscispec *dsciv1.DSCInitializationSpec, platform cluster.Platform, _ bool) error {
	l := c.ConfigComponentLogger(logger, ComponentName, dscispec)

	var imageParamMap = map[string]string{
		"odh-autopilot-controller-image": "RELATED_IMAGE_ODH_AUTOPILOT_IMAGE",
		"namespace":                      dscispec.ApplicationsNamespace,
	}

	enabled := c.GetManagementState() == operatorv1.Managed
	monitoringEnabled := dscispec.Monitoring.ManagementState == operatorv1.Managed

	if enabled {
		if c.DevFlags != nil {
			// Download manifests and update paths
			if err := c.OverrideManifests(ctx, platform); err != nil {
				return err
			}
		}
		if (dscispec.DevFlags == nil || dscispec.DevFlags.ManifestsUri == "") && (c.DevFlags == nil || len(c.DevFlags.Manifests) == 0) {
			if err := deploy.ApplyParams(AutopilotPath, imageParamMap, true); err != nil {
				return err
			}
		}
	}
	// Deploy Training Operator
	if err := deploy.DeployManifestsFromPath(ctx, cli, owner, AutopilotPath, dscispec.ApplicationsNamespace, ComponentName, enabled); err != nil {
		return err
	}
	l.Info("apply manifests done")
	// CloudService Monitoring handling
	if platform == cluster.ManagedRhods {
		if enabled {
			// first check if the service is up, so prometheus wont fire alerts when it is just startup
			if err := cluster.WaitForDeploymentAvailable(ctx, cli, ComponentName, dscispec.ApplicationsNamespace, 20, 2); err != nil {
				return fmt.Errorf("deployment for %s is not ready to server: %w", ComponentName, err)
			}
			fmt.Printf("deployment for %s is done, updating monitoring rules\n", ComponentName)
		}
		l.Info("deployment is done, updating monitoring rules")
		if err := c.UpdatePrometheusConfig(cli, enabled && monitoringEnabled, ComponentName); err != nil {
			return err
		}
		if err := deploy.DeployManifestsFromPath(ctx, cli, owner,
			filepath.Join(deploy.DefaultManifestPath, "monitoring", "prometheus", "apps"),
			dscispec.Monitoring.Namespace,
			"prometheus", true); err != nil {
			return err
		}
		l.Info("updating SRE monitoring done")
	}

	return nil
}
