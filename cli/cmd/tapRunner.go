package cmd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kubeshark/kubeshark/cli/resources"
	"github.com/kubeshark/kubeshark/cli/utils"

	core "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubeshark/kubeshark/cli/apiserver"
	"github.com/kubeshark/kubeshark/cli/cmd/goUtils"
	"github.com/kubeshark/kubeshark/cli/config"
	"github.com/kubeshark/kubeshark/cli/config/configStructs"
	"github.com/kubeshark/kubeshark/cli/errormessage"
	"github.com/kubeshark/kubeshark/cli/uiUtils"
	"github.com/kubeshark/kubeshark/logger"
	"github.com/kubeshark/kubeshark/shared"
	"github.com/kubeshark/kubeshark/shared/kubernetes"
	"github.com/kubeshark/kubeshark/tap/api"
)

const cleanupTimeout = time.Minute

type tapState struct {
	startTime                     time.Time
	targetNamespaces              []string
	kubesharkServiceAccountExists bool
}

var state tapState
var apiProvider *apiserver.Provider

func RunKubesharkTap() {
	state.startTime = time.Now()

	apiProvider = apiserver.NewProvider(GetApiServerUrl(config.Config.Tap.GuiPort), apiserver.DefaultRetries, apiserver.DefaultTimeout)

	kubernetesProvider, err := getKubernetesProviderForCli()
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // cancel will be called when this function exits

	state.targetNamespaces = getNamespaces(kubernetesProvider)

	kubesharkAgentConfig := getTapKubesharkAgentConfig()
	serializedKubesharkConfig, err := getSerializedKubesharkAgentConfig(kubesharkAgentConfig)
	if err != nil {
		logger.Log.Errorf(uiUtils.Error, fmt.Sprintf("Error serializing kubeshark config: %v", errormessage.FormatError(err)))
		return
	}

	if config.Config.IsNsRestrictedMode() {
		if len(state.targetNamespaces) != 1 || !shared.Contains(state.targetNamespaces, config.Config.KubesharkResourcesNamespace) {
			logger.Log.Errorf("Not supported mode. Kubeshark can't resolve IPs in other namespaces when running in namespace restricted mode.\n"+
				"You can use the same namespace for --%s and --%s", configStructs.NamespacesTapName, config.KubesharkResourcesNamespaceConfigName)
			return
		}
	}

	var namespacesStr string
	if !shared.Contains(state.targetNamespaces, kubernetes.K8sAllNamespaces) {
		namespacesStr = fmt.Sprintf("namespaces \"%s\"", strings.Join(state.targetNamespaces, "\", \""))
	} else {
		namespacesStr = "all namespaces"
	}

	logger.Log.Infof("Tapping pods in %s", namespacesStr)

	if err := printTappedPodsPreview(ctx, kubernetesProvider, state.targetNamespaces); err != nil {
		logger.Log.Errorf(uiUtils.Error, fmt.Sprintf("Error listing pods: %v", errormessage.FormatError(err)))
	}

	if config.Config.Tap.DryRun {
		return
	}

	logger.Log.Infof("Waiting for Kubeshark Agent to start...")
	if state.kubesharkServiceAccountExists, err = resources.CreateTapKubesharkResources(ctx, kubernetesProvider, serializedKubesharkConfig, config.Config.IsNsRestrictedMode(), config.Config.KubesharkResourcesNamespace, config.Config.AgentImage, config.Config.Tap.MaxEntriesDBSizeBytes(), config.Config.Tap.ApiServerResources, config.Config.ImagePullPolicy(), config.Config.LogLevel(), config.Config.Tap.Profiler); err != nil {
		var statusError *k8serrors.StatusError
		if errors.As(err, &statusError) && (statusError.ErrStatus.Reason == metav1.StatusReasonAlreadyExists) {
			logger.Log.Info("Kubeshark is already running in this namespace, change the `kubeshark-resources-namespace` configuration or run `kubeshark clean` to remove the currently running Kubeshark instance")
		} else {
			defer resources.CleanUpKubesharkResources(ctx, cancel, kubernetesProvider, config.Config.IsNsRestrictedMode(), config.Config.KubesharkResourcesNamespace)
			logger.Log.Errorf(uiUtils.Error, fmt.Sprintf("Error creating resources: %v", errormessage.FormatError(err)))
		}

		return
	}

	defer finishTapExecution(kubernetesProvider)

	go goUtils.HandleExcWrapper(watchApiServerEvents, ctx, kubernetesProvider, cancel)
	go goUtils.HandleExcWrapper(watchApiServerPod, ctx, kubernetesProvider, cancel)

	// block until exit signal or error
	utils.WaitForFinish(ctx, cancel)
}

func finishTapExecution(kubernetesProvider *kubernetes.Provider) {
	finishKubesharkExecution(kubernetesProvider, config.Config.IsNsRestrictedMode(), config.Config.KubesharkResourcesNamespace)
}

func getTapKubesharkAgentConfig() *shared.KubesharkAgentConfig {
	kubesharkAgentConfig := shared.KubesharkAgentConfig{
		MaxDBSizeBytes:              config.Config.Tap.MaxEntriesDBSizeBytes(),
		InsertionFilter:             config.Config.Tap.GetInsertionFilter(),
		AgentImage:                  config.Config.AgentImage,
		PullPolicy:                  config.Config.ImagePullPolicyStr,
		LogLevel:                    config.Config.LogLevel(),
		TapperResources:             config.Config.Tap.TapperResources,
		KubesharkResourcesNamespace: config.Config.KubesharkResourcesNamespace,
		AgentDatabasePath:           shared.DataDirPath,
		ServiceMap:                  config.Config.ServiceMap,
		OAS:                         config.Config.OAS,
	}

	return &kubesharkAgentConfig
}

/*
this function is a bit problematic as it might be detached from the actual pods the kubeshark api server will tap.
The alternative would be to wait for api server to be ready and then query it for the pods it listens to, this has
the arguably worse drawback of taking a relatively very long time before the user sees which pods are targeted, if any.
*/
func printTappedPodsPreview(ctx context.Context, kubernetesProvider *kubernetes.Provider, namespaces []string) error {
	if matchingPods, err := kubernetesProvider.ListAllRunningPodsMatchingRegex(ctx, config.Config.Tap.PodRegex(), namespaces); err != nil {
		return err
	} else {
		if len(matchingPods) == 0 {
			printNoPodsFoundSuggestion(namespaces)
		}
		for _, tappedPod := range matchingPods {
			logger.Log.Infof(uiUtils.Green, fmt.Sprintf("+%s", tappedPod.Name))
		}
		return nil
	}
}

func startTapperSyncer(ctx context.Context, cancel context.CancelFunc, provider *kubernetes.Provider, targetNamespaces []string, startTime time.Time) error {
	tapperSyncer, err := kubernetes.CreateAndStartKubesharkTapperSyncer(ctx, provider, kubernetes.TapperSyncerConfig{
		TargetNamespaces:            targetNamespaces,
		PodFilterRegex:              *config.Config.Tap.PodRegex(),
		KubesharkResourcesNamespace: config.Config.KubesharkResourcesNamespace,
		AgentImage:                  config.Config.AgentImage,
		TapperResources:             config.Config.Tap.TapperResources,
		ImagePullPolicy:             config.Config.ImagePullPolicy(),
		LogLevel:                    config.Config.LogLevel(),
		KubesharkApiFilteringOptions: api.TrafficFilteringOptions{
			IgnoredUserAgents: config.Config.Tap.IgnoredUserAgents,
		},
		KubesharkServiceAccountExists: state.kubesharkServiceAccountExists,
		ServiceMesh:                   config.Config.Tap.ServiceMesh,
		Tls:                           config.Config.Tap.Tls,
		MaxLiveStreams:                config.Config.Tap.MaxLiveStreams,
	}, startTime)

	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case syncerErr, ok := <-tapperSyncer.ErrorOut:
				if !ok {
					logger.Log.Debug("kubesharkTapperSyncer err channel closed, ending listener loop")
					return
				}
				logger.Log.Errorf(uiUtils.Error, getErrorDisplayTextForK8sTapManagerError(syncerErr))
				cancel()
			case _, ok := <-tapperSyncer.TapPodChangesOut:
				if !ok {
					logger.Log.Debug("kubesharkTapperSyncer pod changes channel closed, ending listener loop")
					return
				}
				if err := apiProvider.ReportTappedPods(tapperSyncer.CurrentlyTappedPods); err != nil {
					logger.Log.Debugf("[Error] failed update tapped pods %v", err)
				}
			case tapperStatus, ok := <-tapperSyncer.TapperStatusChangedOut:
				if !ok {
					logger.Log.Debug("kubesharkTapperSyncer tapper status changed channel closed, ending listener loop")
					return
				}
				if err := apiProvider.ReportTapperStatus(tapperStatus); err != nil {
					logger.Log.Debugf("[Error] failed update tapper status %v", err)
				}
			case <-ctx.Done():
				logger.Log.Debug("kubesharkTapperSyncer event listener loop exiting due to context done")
				return
			}
		}
	}()

	return nil
}

func printNoPodsFoundSuggestion(targetNamespaces []string) {
	var suggestionStr string
	if !shared.Contains(targetNamespaces, kubernetes.K8sAllNamespaces) {
		suggestionStr = ". You can also try selecting a different namespace with -n or tap all namespaces with -A"
	}
	logger.Log.Warningf(uiUtils.Warning, fmt.Sprintf("Did not find any currently running pods that match the regex argument, kubeshark will automatically tap matching pods if any are created later%s", suggestionStr))
}

func getErrorDisplayTextForK8sTapManagerError(err kubernetes.K8sTapManagerError) string {
	switch err.TapManagerReason {
	case kubernetes.TapManagerPodListError:
		return fmt.Sprintf("Failed to update currently tapped pods: %v", err.OriginalError)
	case kubernetes.TapManagerPodWatchError:
		return fmt.Sprintf("Error occured in k8s pod watch: %v", err.OriginalError)
	case kubernetes.TapManagerTapperUpdateError:
		return fmt.Sprintf("Error updating tappers: %v", err.OriginalError)
	default:
		return fmt.Sprintf("Unknown error occured in k8s tap manager: %v", err.OriginalError)
	}
}

func watchApiServerPod(ctx context.Context, kubernetesProvider *kubernetes.Provider, cancel context.CancelFunc) {
	podExactRegex := regexp.MustCompile(fmt.Sprintf("^%s$", kubernetes.ApiServerPodName))
	podWatchHelper := kubernetes.NewPodWatchHelper(kubernetesProvider, podExactRegex)
	eventChan, errorChan := kubernetes.FilteredWatch(ctx, podWatchHelper, []string{config.Config.KubesharkResourcesNamespace}, podWatchHelper)
	isPodReady := false

	apiServerTimeoutSec := config.GetIntEnvConfig(config.ApiServerTimeoutSec, 120)
	timeAfter := time.After(time.Duration(apiServerTimeoutSec) * time.Second)
	for {
		select {
		case wEvent, ok := <-eventChan:
			if !ok {
				eventChan = nil
				continue
			}

			switch wEvent.Type {
			case kubernetes.EventAdded:
				logger.Log.Debugf("Watching API Server pod loop, added")
			case kubernetes.EventDeleted:
				logger.Log.Infof("%s removed", kubernetes.ApiServerPodName)
				cancel()
				return
			case kubernetes.EventModified:
				modifiedPod, err := wEvent.ToPod()
				if err != nil {
					logger.Log.Errorf(uiUtils.Error, err)
					cancel()
					continue
				}

				logger.Log.Debugf("Watching API Server pod loop, modified: %v, containers statuses: %v", modifiedPod.Status.Phase, modifiedPod.Status.ContainerStatuses)

				if modifiedPod.Status.Phase == core.PodRunning && !isPodReady {
					isPodReady = true
					postApiServerStarted(ctx, kubernetesProvider, cancel)
				}
			case kubernetes.EventBookmark:
				break
			case kubernetes.EventError:
				break
			}
		case err, ok := <-errorChan:
			if !ok {
				errorChan = nil
				continue
			}

			logger.Log.Errorf("[ERROR] Agent creation, watching %v namespace, error: %v", config.Config.KubesharkResourcesNamespace, err)
			cancel()

		case <-timeAfter:
			if !isPodReady {
				logger.Log.Errorf(uiUtils.Error, "Kubeshark API server was not ready in time")
				cancel()
			}
		case <-ctx.Done():
			logger.Log.Debugf("Watching API Server pod loop, ctx done")
			return
		}
	}
}

func watchApiServerEvents(ctx context.Context, kubernetesProvider *kubernetes.Provider, cancel context.CancelFunc) {
	podExactRegex := regexp.MustCompile(fmt.Sprintf("^%s", kubernetes.ApiServerPodName))
	eventWatchHelper := kubernetes.NewEventWatchHelper(kubernetesProvider, podExactRegex, "pod")
	eventChan, errorChan := kubernetes.FilteredWatch(ctx, eventWatchHelper, []string{config.Config.KubesharkResourcesNamespace}, eventWatchHelper)
	for {
		select {
		case wEvent, ok := <-eventChan:
			if !ok {
				eventChan = nil
				continue
			}

			event, err := wEvent.ToEvent()
			if err != nil {
				logger.Log.Debugf("[ERROR] parsing Kubeshark resource event: %+v", err)
				continue
			}

			if state.startTime.After(event.CreationTimestamp.Time) {
				continue
			}

			logger.Log.Debugf(
				fmt.Sprintf("Watching API server events loop, event %s, time: %v, resource: %s (%s), reason: %s, note: %s",
					event.Name,
					event.CreationTimestamp.Time,
					event.Regarding.Name,
					event.Regarding.Kind,
					event.Reason,
					event.Note))

			switch event.Reason {
			case "FailedScheduling", "Failed":
				logger.Log.Errorf(uiUtils.Error, fmt.Sprintf("Kubeshark API Server status: %s - %s", event.Reason, event.Note))
				cancel()

			}
		case err, ok := <-errorChan:
			if !ok {
				errorChan = nil
				continue
			}

			logger.Log.Debugf("[Error] Watching API server events loop, error: %+v", err)
		case <-ctx.Done():
			logger.Log.Debugf("Watching API server events loop, ctx done")
			return
		}
	}
}

func postApiServerStarted(ctx context.Context, kubernetesProvider *kubernetes.Provider, cancel context.CancelFunc) {
	startProxyReportErrorIfAny(kubernetesProvider, ctx, cancel, config.Config.Tap.GuiPort)

	if err := startTapperSyncer(ctx, cancel, kubernetesProvider, state.targetNamespaces, state.startTime); err != nil {
		logger.Log.Errorf(uiUtils.Error, fmt.Sprintf("Error starting kubeshark tapper syncer: %v", errormessage.FormatError(err)))
		cancel()
	}

	url := GetApiServerUrl(config.Config.Tap.GuiPort)
	logger.Log.Infof("Kubeshark is available at %s", url)
	if !config.Config.HeadlessMode {
		uiUtils.OpenBrowser(url)
	}
}

func getNamespaces(kubernetesProvider *kubernetes.Provider) []string {
	if config.Config.Tap.AllNamespaces {
		return []string{kubernetes.K8sAllNamespaces}
	} else if len(config.Config.Tap.Namespaces) > 0 {
		return shared.Unique(config.Config.Tap.Namespaces)
	} else {
		currentNamespace, err := kubernetesProvider.CurrentNamespace()
		if err != nil {
			logger.Log.Fatalf(uiUtils.Red, fmt.Sprintf("error getting current namespace: %+v", err))
		}
		return []string{currentNamespace}
	}
}
