package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kindlingvm/kindling/internal/builder"
	ciworker "github.com/kindlingvm/kindling/internal/ci"
	"github.com/kindlingvm/kindling/internal/config"
	"github.com/kindlingvm/kindling/internal/database"
	"github.com/kindlingvm/kindling/internal/database/queries"
	"github.com/kindlingvm/kindling/internal/deploy"
	"github.com/kindlingvm/kindling/internal/migrationreconcile"
	"github.com/kindlingvm/kindling/internal/reconciler"
	crunrt "github.com/kindlingvm/kindling/internal/runtime"
	"github.com/kindlingvm/kindling/internal/sandbox"
	"github.com/kindlingvm/kindling/internal/serverreconcile"
	"github.com/kindlingvm/kindling/internal/usage"
	"github.com/kindlingvm/kindling/internal/volumeops"
)

// reconcilers holds all reconciler schedulers created for the worker component.
type reconcilers struct {
	deployment *reconciler.Scheduler
	build      *reconciler.Scheduler
	ciJob      *reconciler.Scheduler
	vm         *reconciler.Scheduler
	domain     *reconciler.Scheduler
	server     *reconciler.Scheduler
	migration  *reconciler.Scheduler
	volumeOp   *reconciler.Scheduler
	sandbox    *reconciler.Scheduler
	sandboxTpl *reconciler.Scheduler
}

// workerSetupResult bundles the outputs from setupWorker.
type workerSetupResult struct {
	rt         crunrt.Runtime
	deployer   *deploy.Deployer
	ciSvc      *ciworker.JobService
	sandboxSvc *sandbox.Service
	recs       reconcilers
}

// setupWorker initialises the runtime, builder, deployer, and all reconciler
// schedulers needed by the worker component. It does not start any goroutines.
func setupWorker(
	ctx context.Context,
	q *queries.Queries,
	db *database.DB,
	serverID uuid.UUID,
	cfgMgr *config.Manager,
	snap *config.Snapshot,
	notifyRouteChange func(),
) (workerSetupResult, error) {
	pullAuth := registryAuthFromSnapshot(snap)
	rt := crunrt.NewDetectedRuntime(crunrt.HostRuntimeConfig{
		ForceRuntime:  snap.ServerRuntimeOverride,
		AdvertiseHost: snap.ServerAdvertiseHost,
		PullAuth:      pullAuth,
		CloudHypervisor: crunrt.CloudHypervisorHostConfig{
			BinaryPath:    snap.ServerCloudHypervisorBin,
			KernelPath:    snap.ServerCloudHypervisorKernelPath,
			InitramfsPath: snap.ServerCloudHypervisorInitramfsPath,
			StateDir:      snap.ServerCloudHypervisorStateDir,
		},
		AppleKernelPath:    "",
		AppleInitramfsPath: "",
	})
	slog.Info("runtime detected", "runtime", rt.Name())

	var buildRunner builder.BuildRunner = builder.NewLocalBuildRunner()
	if runtime.GOOS == "darwin" {
		home, herr := os.UserHomeDir()
		if herr == nil {
			ar, err := builder.NewAppleVZBuildRunner(builder.AppleVZBuildRunnerConfig{
				KernelPath:       filepath.Join(home, ".kindling", "vmlinuz.bin"),
				InitramfsPath:    filepath.Join(home, ".kindling", "initramfs.cpio.gz"),
				BuilderRootfsDir: filepath.Join(home, ".kindling", "builder-rootfs"),
			})
			if err != nil {
				slog.Warn("apple vz oci build runner disabled", "error", err)
			} else {
				buildRunner = ar
				slog.Info("using Apple VZ builder VM for OCI builds")
			}
		}
	}
	bldr := builder.New(func(c context.Context) (builder.Config, error) {
		s := cfgMgr.Snapshot()
		if s == nil {
			return builder.Config{}, fmt.Errorf("config snapshot unavailable")
		}
		return builder.Config{
			GitHubToken:      s.GitHubToken,
			RegistryURL:      s.RegistryURL,
			RegistryUsername: s.RegistryUsername,
			RegistryPassword: s.RegistryPassword,
		}, nil
	}, q, serverID, buildRunner)

	deployer := deploy.New(q, db.Pool, serverID, cfgMgr)
	deployer.SetRuntime(rt)

	deploymentReconciler := reconciler.New(reconciler.Config{
		Name:      "deployment",
		Reconcile: failFastOnClosedPool("deployment", deployer.ReconcileDeployment),
	})
	deployer.SetReconciler(deploymentReconciler)

	buildReconciler := reconciler.New(reconciler.Config{
		Name:      "build",
		Reconcile: failFastOnClosedPool("build", bldr.ReconcileBuild),
	})

	ciSvc := ciworker.NewJobService(q, cfgMgr, serverID)
	ciJobReconciler := reconciler.New(reconciler.Config{
		Name:      "ci_job",
		Reconcile: failFastOnClosedPool("ci_job", ciSvc.Reconcile),
	})

	vmReconciler := reconciler.New(reconciler.Config{
		Name: "instance",
		Reconcile: func(ctx context.Context, id uuid.UUID) error {
			slog.Info("reconciling instance", "id", id)
			return nil
		},
	})

	domainReconciler := reconciler.New(reconciler.Config{
		Name: "domain",
		Reconcile: func(ctx context.Context, id uuid.UUID) error {
			slog.Info("reconciling domain", "id", id)
			notifyRouteChange()
			return nil
		},
	})

	serverDrainHandler := serverreconcile.NewHandler(q, deploymentReconciler, notifyRouteChange)
	migrationHandler := migrationreconcile.NewHandler(q, db.Pool, rt, serverID, deploymentReconciler, notifyRouteChange)
	volumeHandler := volumeops.NewHandler(q, cfgMgr, serverID)

	serverReconciler := reconciler.New(reconciler.Config{
		Name:      "server",
		Reconcile: failFastOnClosedPool("server", serverDrainHandler.Reconcile),
	})
	migrationReconciler := reconciler.New(reconciler.Config{
		Name:      "instance_migration",
		Reconcile: failFastOnClosedPool("instance_migration", migrationHandler.Reconcile),
	})
	volumeOpReconciler := reconciler.New(reconciler.Config{
		Name:      "project_volume_operation",
		Reconcile: failFastOnClosedPool("project_volume_operation", volumeHandler.Reconcile),
	})
	deployer.SetServerScheduler(serverReconciler)

	sandboxSvc := &sandbox.Service{
		Q:        q,
		Runtime:  rt,
		ServerID: serverID,
	}
	sandboxReconciler := reconciler.New(reconciler.Config{
		Name:      "remote_vm",
		Reconcile: failFastOnClosedPool("remote_vm", sandboxSvc.Reconcile),
	})
	sandboxTemplateReconciler := reconciler.New(reconciler.Config{
		Name:      "remote_vm_template",
		Reconcile: failFastOnClosedPool("remote_vm_template", sandboxSvc.ReconcileTemplate),
	})

	return workerSetupResult{
		rt:         rt,
		deployer:   deployer,
		ciSvc:      ciSvc,
		sandboxSvc: sandboxSvc,
		recs: reconcilers{
			deployment: deploymentReconciler,
			build:      buildReconciler,
			ciJob:      ciJobReconciler,
			vm:         vmReconciler,
			domain:     domainReconciler,
			server:     serverReconciler,
			migration:  migrationReconciler,
			volumeOp:   volumeOpReconciler,
			sandbox:    sandboxReconciler,
			sandboxTpl: sandboxTemplateReconciler,
		},
	}, nil
}

func failFastOnClosedPool(name string, reconcileFn reconciler.ReconcileFunc) reconciler.ReconcileFunc {
	return func(ctx context.Context, id uuid.UUID) error {
		err := reconcileFn(ctx, id)
		if err != nil {
			maybeExitForClosedPool(err, name+" reconcile")
		}
		return err
	}
}

// startReconcilers launches all reconciler goroutines and performs startup
// recovery for deployments and volumes.
func startReconcilers(ctx context.Context, q *queries.Queries, serverID uuid.UUID, rt crunrt.Runtime, recs reconcilers, notifyRouteChange func()) {
	go recs.deployment.Start(ctx)
	go recs.build.Start(ctx)
	go recs.ciJob.Start(ctx)
	go recs.vm.Start(ctx)
	go recs.domain.Start(ctx)
	go recs.server.Start(ctx)
	go recs.migration.Start(ctx)
	go recs.volumeOp.Start(ctx)
	go recs.sandbox.Start(ctx)
	go recs.sandboxTpl.Start(ctx)
	slog.Info("reconcilers started")

	if retainedRT, ok := rt.(crunrt.DurableRetainedStateRuntime); ok {
		if err := recoverWorkerRetainedState(ctx, q, serverID, retainedRT); err != nil {
			slog.Warn("startup retained state recovery skipped", "error", err)
		}
	}

	recoveredDeployments, err := queueStartupRecovery(ctx, q, serverID, recs.deployment, notifyRouteChange)
	if err != nil {
		slog.Warn("startup deployment recovery skipped", "error", err)
	} else if recoveredDeployments > 0 {
		slog.Info("startup deployment recovery queued", "server_id", serverID, "deployments", recoveredDeployments)
	}
	go runProjectVolumeOperationRecoveryLoop(ctx, q, recs.volumeOp)
}

// startWorkerHeartbeats launches background heartbeats and resource polling for
// the worker component.
func startWorkerHeartbeats(ctx context.Context, q *queries.Queries, serverID uuid.UUID, rt crunrt.Runtime) {
	go runServerComponentHeartbeat(ctx, q, serverID, "worker", componentHeartbeatInterval, func() map[string]any {
		meta := map[string]any{"runtime": rt.Name()}
		meta["remote_vm_enabled"] = rt.Name() == "cloud-hypervisor" || rt.Name() == "apple-vz"
		meta["remote_vm_backend"] = rt.Name()
		meta["remote_vm_arch"] = runtime.GOARCH
		meta["remote_vm_rosetta"] = false
		meta["remote_vm_capacity"] = 1
		if rt.Name() == "cloud-hypervisor" {
			meta["live_migration_enabled"] = rt.Supports(crunrt.CapabilityLiveMigration)
			if v := cloudHypervisorVersion(); v != "" {
				meta["cloud_hypervisor_version"] = v
			}
			if chrt, ok := rt.(crunrt.DurableRetainedStateRuntime); ok {
				if v := strings.TrimSpace(chrt.StateDir()); v != "" {
					meta["state_dir"] = v
				}
				meta["durable_fast_wake_enabled"] = chrt.DurableFastWakeEnabled()
			}
			if v := strings.TrimSpace(os.Getenv("KINDLING_CH_SHARED_ROOTFS_DIR")); v != "" {
				meta["shared_rootfs_dir"] = v
			}
			for key, value := range internalDNSRuntimeMetadata(rt.Name()) {
				meta[key] = value
			}
		}
		return meta
	})
	go usage.RunResourcePoller(ctx, q, serverID, rt, usagePollerInterval, func(report usage.PollerStatusReport) {
		if err := persistServerComponentStatus(ctx, q, serverID, serverComponentStatusUpdate{
			Component:        "usage_poller",
			Status:           report.Status,
			ObservedAt:       report.ObservedAt,
			LastSuccessAt:    report.LastSuccessAt,
			LastErrorAt:      report.LastErrorAt,
			LastErrorMessage: report.LastErrorMessage,
			Metadata:         report.Metadata,
		}); err != nil && ctx.Err() == nil {
			slog.Warn("usage poller component status", "error", err)
		}
	})
}

// registerServerAndHeartbeat registers the server in the database and starts the
// periodic heartbeat loop. Call when the worker or edge component is enabled.
func registerServerAndHeartbeat(ctx context.Context, q *queries.Queries, serverID uuid.UUID) error {
	ipRange, err := parseServerIPRange()
	if err != nil {
		return err
	}
	_, err = q.ServerRegister(ctx, queries.ServerRegisterParams{
		ID:         pgtype.UUID{Bytes: serverID, Valid: true},
		Hostname:   hostname(),
		InternalIp: detectInternalIP(),
		IpRange:    ipRange,
	})
	if err != nil {
		return fmt.Errorf("register server: %w", err)
	}
	slog.Info("server registered", "server_id", serverID)

	go runServerHeartbeat(ctx, q, serverID)

	if err := q.ServerSettingEnsure(ctx, pgtype.UUID{Bytes: serverID, Valid: true}); err != nil {
		return fmt.Errorf("ensure server settings: %w", err)
	}
	return nil
}
