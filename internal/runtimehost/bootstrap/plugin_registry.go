package bootstrap

import (
	"context"
	"errors"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/adaptercatalog"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/generation"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/httpapi"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/installation"
	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/pluginregistry"
)

type sourceAwareAdapterCatalog interface {
	ReconcileWithSources(context.Context, string, []string) (adaptercatalog.Snapshot, error)
}

func (c *updateController) reconcileCatalog(ctx context.Context, fallbackSetID string) (adaptercatalog.Snapshot, error) {
	var sources []string
	if c.pluginRegistry != nil {
		var err error
		sources, err = c.pluginRegistry.DesiredSourceDirs()
		if err != nil {
			return adaptercatalog.Snapshot{}, err
		}
	}
	if len(sources) == 0 {
		return c.adapterCatalog.Reconcile(ctx, fallbackSetID)
	}
	withSources, ok := c.adapterCatalog.(sourceAwareAdapterCatalog)
	if !ok {
		return adaptercatalog.Snapshot{}, errors.New("adapter catalog does not support Host-owned plugin sources")
	}
	return withSources.ReconcileWithSources(ctx, fallbackSetID, sources)
}

func (c *updateController) PluginStatus(ctx context.Context) (httpapi.PluginStatus, error) {
	if ctx == nil || ctx.Err() != nil {
		return httpapi.PluginStatus{}, httpapi.NewControllerError("internal_error")
	}
	return c.pluginStatus(), nil
}

func (c *updateController) RefreshPlugins(ctx context.Context) (httpapi.PluginStatus, error) {
	operationCtx, cancel, err := c.beginPluginOperation(ctx)
	if err != nil {
		return httpapi.PluginStatus{}, err
	}
	defer cancel()
	defer c.releaseOperation()
	if c.pluginRegistry == nil {
		return httpapi.PluginStatus{}, httpapi.NewControllerError("plugin_registry_unavailable")
	}
	if err := c.pluginRegistry.Refresh(operationCtx); err != nil {
		return c.pluginStatus(), mapPluginRegistryError(err)
	}
	return c.pluginStatus(), nil
}

func (c *updateController) InstallPlugin(ctx context.Context, id string) (httpapi.PluginStatus, error) {
	return c.changePlugin(ctx, id, false)
}

func (c *updateController) UpdatePlugin(ctx context.Context, id string) (httpapi.PluginStatus, error) {
	return c.changePlugin(ctx, id, true)
}

func (c *updateController) UninstallPlugin(ctx context.Context, id string) (httpapi.PluginStatus, error) {
	operationCtx, cancel, err := c.beginPluginOperation(ctx)
	if err != nil {
		return httpapi.PluginStatus{}, err
	}
	defer cancel()
	defer c.releaseOperation()
	if c.pluginRegistry == nil {
		return httpapi.PluginStatus{}, httpapi.NewControllerError("plugin_registry_unavailable")
	}
	if c.registry.Snapshot().StagedGenerationID != "" {
		return httpapi.PluginStatus{}, httpapi.NewControllerError("plugin_operation_conflict")
	}
	plan, err := c.pluginRegistry.PrepareUninstall(id)
	if err != nil {
		return httpapi.PluginStatus{}, mapPluginRegistryError(err)
	}
	defer func() {
		plan.Close()
		_ = c.pluginRegistry.CollectGarbage()
	}()
	if err := plan.Commit(); err != nil {
		return httpapi.PluginStatus{}, mapPluginRegistryError(err)
	}
	result, err := c.reconcileAdaptersLocked(operationCtx)
	if err != nil || result.State != "activated" && result.State != "unchanged" && result.State != "rejected" {
		_ = plan.Rollback()
		return httpapi.PluginStatus{}, httpapi.NewControllerError("plugin_install_failed")
	}
	if err := c.verifyActivePluginAbsent(id); err != nil {
		_ = plan.Rollback()
		return httpapi.PluginStatus{}, httpapi.NewControllerError("plugin_install_failed")
	}
	return c.pluginStatus(), nil
}

func (c *updateController) changePlugin(ctx context.Context, id string, update bool) (httpapi.PluginStatus, error) {
	operationCtx, cancel, err := c.beginPluginOperation(ctx)
	if err != nil {
		return httpapi.PluginStatus{}, err
	}
	defer cancel()
	defer c.releaseOperation()
	if c.pluginRegistry == nil {
		return httpapi.PluginStatus{}, httpapi.NewControllerError("plugin_registry_unavailable")
	}
	if c.registry.Snapshot().StagedGenerationID != "" {
		return httpapi.PluginStatus{}, httpapi.NewControllerError("plugin_operation_conflict")
	}
	plan, err := c.pluginRegistry.PrepareInstall(operationCtx, id, update)
	if err != nil {
		return httpapi.PluginStatus{}, mapPluginRegistryError(err)
	}
	defer func() {
		plan.Close()
		_ = c.pluginRegistry.CollectGarbage()
	}()
	selection := plan.Selection()
	if err := plan.Commit(); err != nil {
		return httpapi.PluginStatus{}, mapPluginRegistryError(err)
	}
	result, err := c.reconcileAdaptersLocked(operationCtx)
	if err != nil || result.State != "activated" && result.State != "unchanged" && result.State != "rejected" {
		_ = plan.Rollback()
		return httpapi.PluginStatus{}, httpapi.NewControllerError("plugin_install_failed")
	}
	if err := c.verifyActivePlugin(selection); err != nil {
		_ = plan.Rollback()
		return httpapi.PluginStatus{}, httpapi.NewControllerError("plugin_identity_mismatch")
	}
	return c.pluginStatus(), nil
}

func (c *updateController) beginPluginOperation(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, httpapi.NewControllerError("internal_error")
	}
	operationCtx, cancel := context.WithTimeout(ctx, c.operationTimeout)
	if c.installation == nil || c.installation.Snapshot().State != installation.StateReady {
		cancel()
		return nil, nil, httpapi.NewControllerError("installation_incomplete")
	}
	if err := c.lockOperation(operationCtx, true); err != nil {
		cancel()
		return nil, nil, httpapi.NewControllerError("plugin_operation_conflict")
	}
	if operationCtx.Err() != nil {
		c.releaseOperation()
		cancel()
		return nil, nil, httpapi.NewControllerError("plugin_operation_conflict")
	}
	return operationCtx, cancel, nil
}

func (c *updateController) pluginStatus() httpapi.PluginStatus {
	if c.pluginRegistry == nil {
		return httpapi.PluginStatus{State: "not_configured", Plugins: []httpapi.PluginStatusItem{}}
	}
	view := c.pluginRegistry.View()
	status := httpapi.PluginStatus{State: "ready", Plugins: make([]httpapi.PluginStatusItem, 0, len(view.Plugins))}
	if !view.Configured {
		status.State = "not_configured"
	} else if !view.Available {
		status.State = "unavailable"
		status.FailureCode = "plugin_registry_unavailable"
	}
	for _, item := range view.Plugins {
		status.Plugins = append(status.Plugins, httpapi.PluginStatusItem{
			ID: item.ID, Name: item.Name, AvailableVersion: item.AvailableVersion,
			InstalledVersion: item.InstalledVersion, Installed: item.Installed,
			UpdateAvailable: item.UpdateAvailable,
		})
	}
	return status
}

func (c *updateController) verifyActivePlugin(selection pluginregistry.DesiredPlugin) error {
	if selection.ID == "" || c.adapterCatalog == nil || c.registry == nil {
		return pluginregistry.ErrIdentityMismatch
	}
	snapshot := c.registry.Snapshot()
	active, ok := snapshot.Generations[snapshot.ActiveGenerationID]
	if !ok || active.State != generation.StateActive || active.AdapterSetID == "" {
		return pluginregistry.ErrIdentityMismatch
	}
	set, err := c.adapterCatalog.Load(active.AdapterSetID)
	if err != nil {
		return pluginregistry.ErrIdentityMismatch
	}
	for _, entry := range set.Entries {
		if entry.AdapterID == selection.ID && entry.Version == selection.Version && entry.ProtocolVersion == adapterproto.Version && entry.ArtifactSHA256 == selection.Digest {
			return nil
		}
	}
	return pluginregistry.ErrIdentityMismatch
}

func (c *updateController) verifyActivePluginAbsent(id string) error {
	if id == "" || c.adapterCatalog == nil || c.registry == nil {
		return pluginregistry.ErrIdentityMismatch
	}
	snapshot := c.registry.Snapshot()
	active, ok := snapshot.Generations[snapshot.ActiveGenerationID]
	if !ok || active.State != generation.StateActive || active.AdapterSetID == "" {
		return pluginregistry.ErrIdentityMismatch
	}
	set, err := c.adapterCatalog.Load(active.AdapterSetID)
	if err != nil {
		return pluginregistry.ErrIdentityMismatch
	}
	for _, entry := range set.Entries {
		if entry.AdapterID == id {
			return pluginregistry.ErrIdentityMismatch
		}
	}
	return nil
}

func mapPluginRegistryError(err error) error {
	switch {
	case errors.Is(err, pluginregistry.ErrPluginNotFound), errors.Is(err, pluginregistry.ErrNotInstalled):
		return httpapi.NewControllerError("plugin_not_found")
	case errors.Is(err, pluginregistry.ErrPlatformUnsupported):
		return httpapi.NewControllerError("plugin_platform_unsupported")
	case errors.Is(err, pluginregistry.ErrDownloadFailed):
		return httpapi.NewControllerError("plugin_download_failed")
	case errors.Is(err, pluginregistry.ErrVerificationFailed):
		return httpapi.NewControllerError("plugin_verification_failed")
	case errors.Is(err, pluginregistry.ErrIdentityMismatch):
		return httpapi.NewControllerError("plugin_identity_mismatch")
	case errors.Is(err, pluginregistry.ErrUnavailable):
		return httpapi.NewControllerError("plugin_registry_unavailable")
	case errors.Is(err, pluginregistry.ErrAlreadyInstalled), errors.Is(err, pluginregistry.ErrNoUpdateAvailable), errors.Is(err, pluginregistry.ErrOperationConflict):
		return httpapi.NewControllerError("plugin_operation_conflict")
	default:
		return httpapi.NewControllerError("plugin_install_failed")
	}
}
