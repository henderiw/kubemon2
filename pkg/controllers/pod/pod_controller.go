package pod

import (
	"context"
	"fmt"
	"sync"

	"github.com/henderiw/kubemon2/pkg/controllers/controller"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	uruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
)

// WorkloadEndpointCache struct
type WorkloadEndpointCache struct {
	sync.RWMutex
}

// podController implements the Controller interface for managing Kubernetes pods
// and syncing them to the datastore as WorkloadEndpoints.
type podController struct {
	indexer  cache.Indexer
	informer cache.Controller
	ctx      context.Context
	queue    workqueue.RateLimitingInterface
}

// NewPodController returns a controller which manages Pod objects.
func NewPodController(ctx context.Context, k8sClientset *kubernetes.Clientset) controller.Controller {
	// Create a Pod watcher.
	listWatcher := cache.NewListWatchFromClient(k8sClientset.CoreV1().RESTClient(), "pods", "", fields.Everything())

	// create the workqueue
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	// Bind the cache to kubernetes cache with the help of an informer. This way we make sure that
	// whenever the kubernetes cache is updated, changes get reflected in the cache as well.
	indexer, informer := cache.NewIndexerInformer(listWatcher, &v1.Pod{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err != nil {
				log.WithError(err).Error("Failed to generate key")
				return
			}
			queue.Add(key)
			log.Info("Got ADD event for pod: %s", key)

			// Ignore updates for host networked pods.
			if isHostNetworked(obj.(*v1.Pod)) {
				log.Debugf("Skipping irrelevant pod %s", key)
				return
			}

		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(newObj)
			if err != nil {
				log.WithError(err).Error("Failed to generate key")
				return
			}
			queue.Add(key)
			log.Info("Got UPDATE event for pod: %s", key)

			// Ignore updates for not ready / irrelevant pods.
			if !isReadyPod(newObj.(*v1.Pod)) {
				log.Debugf("Skipping irrelevant pod %s", key)
				return
			}

		},
		DeleteFunc: func(obj interface{}) {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				log.WithError(err).Error("Failed to generate key")
				return
			}
			queue.Add(key)
			log.Info("Got DELETE event for pod: %s", key)

			// Ignore updates for host networked pods.
			if isHostNetworked(obj.(*v1.Pod)) {
				log.Debugf("Skipping irrelevant pod %s", key)
				return
			}

		},
	}, cache.Indexers{})

	return &podController{indexer, informer, ctx, queue}
}

// Run starts the controller.
func (c *podController) Run(threadiness int, reconcilerPeriod string, stopCh chan struct{}) {
	defer uruntime.HandleCrash()

	// Let the workers stop when we are done
	defer c.queue.ShutDown()

	log.Info("Starting Pod/WorkloadEndpoint controller")

	// Wait till k8s cache is synced
	go c.informer.Run(stopCh)
	log.Debug("Waiting to sync with Kubernetes API (Pods)")

	for !c.informer.HasSynced() {
	}
	log.Debug("Finished syncing with Kubernetes API (Pods)")

	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	// Start a number of worker threads to read from the queue.
	for i := 0; i < threadiness; i++ {
		go c.runWorker()
	}
	log.Info("Pod/WorkloadEndpoint controller is now running")

	<-stopCh
	log.Info("Stopping Pod controller")
}

func (c *podController) runWorker() {
	for c.processNextItem() {
	}
}

// processNextItem waits for an event on the output queue from the resource cache and syncs
// any received keys to the datastore.
func (c *podController) processNextItem() bool {
	// Wait until there is a new item in the work queue.
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	// Indicate that we're done processing this key, allowing for safe parallel processing such that
	// two objects with the same key are never processed in parallel.
	defer c.queue.Done(key)

	// Invoke the method containing the business logic
	err := c.syncToStdout(key.(string))
	// Handle the error if something went wrong during the execution of the business logic
	c.handleErr(err, key)

	return true
}

// syncToStdout is the business logic of the controller. In this controller it simply prints
// information about the pod to stdout. In case an error happened, it has to simply return the error.
// The retry logic should not be part of the business logic.
func (c *podController) syncToStdout(key string) error {
	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		klog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		// Below we will warm up our cache with a Pod, so that we will see a delete for one pod
		log.Info("Pod %s does not exist anymore", key)
	} else {
		// Note that you also have to check the uid if you have a local controlled resource, which
		// is dependent on the actual instance, to detect that a Pod was recreated with the same name
		log.Info("Sync/Add/Update for Pod %s", obj.(*v1.Pod).GetName())
	}
	return nil
}

// handleErr handles errors which occur while processing a key received from the resource cache.
// For a given error, we will re-queue the key in order to retry the datastore sync up to 5 times,
// at which point the update is dropped.
func (c *podController) handleErr(err error, key interface{}) {
	if err == nil {
		// Forget about the #AddRateLimited history of the key on every successful synchronization.
		// This ensures that future processing of updates for this key is not delayed because of
		// an outdated error history.
		log.WithField("key", key).Debug("Error for key is no more, drop from retry queue")
		c.queue.Forget(key)
		return
	}

	// This controller retries 5 times if something goes wrong. After that, it stops trying.
	if c.queue.NumRequeues(key) < 5 {
		log.WithError(err).Errorf("Error syncing pod, will retry: %v: %v", key, err)
		// Re-enqueue the key rate limited. Based on the rate limiter on the
		// queue and the re-enqueue history, the key will be processed later again.
		c.queue.AddRateLimited(key)
		return
	}
	c.queue.Forget(key)

	// Report to an external entity that, even after several retries, we could not successfully process this key
	uruntime.HandleError(err)
	log.WithError(err).Errorf("Dropping pod %q out of the retry queue: %v", key, err)
}

func isReadyPod(pod *v1.Pod) bool {
	if isHostNetworked(pod) {
		log.WithField("pod", pod.Name).Debug("Pod is host networked.")
		return false
	} else if !hasIPAddress(pod) {
		log.WithField("pod", pod.Name).Debug("Pod does not have an IP address.")
		return false
	} else if !isScheduled(pod) {
		log.WithField("pod", pod.Name).Debug("Pod is not scheduled.")
		return false
	}
	return true
}

func isScheduled(pod *v1.Pod) bool {
	return pod.Spec.NodeName != ""
}

func isHostNetworked(pod *v1.Pod) bool {
	return pod.Spec.HostNetwork
}

func hasIPAddress(pod *v1.Pod) bool {
	return pod.Status.PodIP != ""
}
