package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/henderiw/kubemon2/lib/logutils"
	"github.com/henderiw/kubemon2/pkg/config"
	"github.com/henderiw/kubemon2/pkg/controllers/controller"
	"github.com/henderiw/kubemon2/pkg/controllers/namespace"
	"github.com/henderiw/kubemon2/pkg/controllers/pod"

	"github.com/henderiw/kubemon2/pkg/status"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

// VERSION is filled out during the build process (using git describe output)
var VERSION string
var version bool
var kubeconfig string

func init() {
	// Add a flag to check the version.
	flag.BoolVar(&version, "version", false, "Display version")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")

	// Tell klog to log into STDERR. Otherwise, we risk
	// certain kinds of API errors getting logged into a directory not
	// available in a `FROM scratch` Docker container, causing us to abort
	var flags flag.FlagSet
	klog.InitFlags(&flags)
	err := flags.Set("logtostderr", "true")
	if err != nil {
		log.WithError(err).Fatal("Failed to set klog logging configuration")
	}
}

func main() {
	flag.Parse()
	if version {
		log.Info("VERSION %s", VERSION)
		os.Exit(0)
	}

	// Configure log formatting.
	log.SetFormatter(&logutils.Formatter{})

	// Install a hook that adds file/line no information.
	log.AddHook(&logutils.ContextHook{})

	// Attempt to load configuration.
	/*
		config := new(config.Config)
		if err := config.Parse(); err != nil {
			log.WithError(err).Fatal("Failed to parse config")
		}
		log.WithField("config", config).Info("Loaded configuration from environment")
	*/

	// Set the log level based on the loaded configuration.
	/*
		logLevel, err := log.ParseLevel(config.LogLevel)
		if err != nil {
			logLevel = log.InfoLevel
		}
	*/
	//log.SetLevel(5)

	// Build clients to be used by the controllers.
	if kubeconfig == "" {
		kubeconfig = "/app/config"
		log.Info("kubeconfig: '%s' provided.", kubeconfig)
	}
	k8sClientset, err := getClients(kubeconfig)
	//k8sClientset, err := getClients(config.Kubeconfig)
	if err != nil {
		log.WithError(err).Fatal("Failed to start")
	}

	stop := make(chan struct{})
	defer close(stop)

	// Create the context.
	ctx := context.Background()

	controllerCtrl := &controllerControl{
		ctx:              ctx,
		controllerStates: make(map[string]*controllerState),
		config:           nil,
		stop:             stop,
		//config:           config,
	}

	// Create the status file. We will only update it if we have healthchecks enabled.
	s := status.New(status.DefaultStatusFile)

	for _, controllerType := range strings.Split("namespace,workloadendpoint", ",") {
		switch controllerType {
		case "workloadendpoint":
			podController := pod.NewPodController(ctx, k8sClientset)
			controllerCtrl.controllerStates["Pod"] = &controllerState{
				controller:  podController,
				threadiness: 1,
			}
		case "profile", "namespace":
			namespaceController := namespace.NewNamespaceController(ctx, k8sClientset)
			controllerCtrl.controllerStates["Namespace"] = &controllerState{
				controller:  namespaceController,
				threadiness: 1,
			}
		default:
			log.Fatalf("Invalid controller '%s' provided.", controllerType)
		}
	}

	// Run the health checks on a separate goroutine.
	if true {
		log.Info("Starting status report routine")
		go runHealthChecks(ctx, s, k8sClientset)
	}

	// Run the controllers. This runs indefinitely.
	controllerCtrl.RunControllers()
}

// Run the controller health checks.
func runHealthChecks(ctx context.Context, s *status.Status, k8sClientset *kubernetes.Clientset) {
	s.SetReady("KubeAPIServer", false, "initialized to false")

	// Loop forever and perform healthchecks.
	for {
		// Kube-apiserver HealthCheck
		healthStatus := 0
		k8sCheckDone := make(chan interface{}, 1)
		go func(k8sCheckDone <-chan interface{}) {
			time.Sleep(2 * time.Second)
			select {
			case <-k8sCheckDone:
				// The check has completed.
			default:
				// Check is still running, so report not ready.
				s.SetReady(
					"KubeAPIServer",
					false,
					fmt.Sprintf("Error reaching apiserver: taking a long time to check apiserver"),
				)
			}
		}(k8sCheckDone)
		k8sClientset.Discovery().RESTClient().Get().AbsPath("/healthz").Do().StatusCode(&healthStatus)
		k8sCheckDone <- nil
		if healthStatus != http.StatusOK {
			log.WithError(nil).Errorf("Failed to reach apiserver")
			s.SetReady(
				"KubeAPIServer",
				false,
				fmt.Sprintf("Error reaching apiserver: %v with http status code: %d", nil, healthStatus),
			)
		} else {
			s.SetReady("KubeAPIServer", true, "")
		}

		time.Sleep(10 * time.Second)
	}
}

// getClients builds and returns Kubernetes and Calico clients.
func getClients(kubeconfig string) (*kubernetes.Clientset, error) {

	// Build the Kubernetes client, we support in-cluster config and kubeconfig
	// as means of configuring the client.
	k8sconfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubernetes client config: %s", err)
	}

	// Get Kubernetes clientset
	k8sClientset, err := kubernetes.NewForConfig(k8sconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubernetes client: %s", err)
	}

	return k8sClientset, nil
}

// Object for keeping track of controller states and statuses.
type controllerControl struct {
	ctx              context.Context
	controllerStates map[string]*controllerState
	config           *config.Config
	stop             chan struct{}
}

// Runs all the controllers and blocks indefinitely.
func (cc *controllerControl) RunControllers() {
	for controllerType, cs := range cc.controllerStates {
		log.WithField("ControllerType", controllerType).Info("Starting controller")
		go cs.controller.Run(cs.threadiness, "5m", cc.stop)
	}
	select {}
}

// Object for keeping track of Controller information.
type controllerState struct {
	controller  controller.Controller
	threadiness int
}
