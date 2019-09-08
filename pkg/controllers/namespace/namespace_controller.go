package namespace

import (
	"context"
	"fmt"

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

// namespaceController implements the Controller interface for managing Kubernetes namespaces
// and syncing them to the Calico datastore as Profiles.
type namespaceController struct {
	indexer  cache.Indexer
	informer cache.Controller
	ctx      context.Context
	queue    workqueue.RateLimitingInterface
}

// NewNamespaceController returns a controller which manages Namespace objects.
func NewNamespaceController(ctx context.Context, k8sClientset *kubernetes.Clientset) controller.Controller {

	// Create a Namespace watcher.
	listWatcher := cache.NewListWatchFromClient(k8sClientset.CoreV1().RESTClient(), "namespaces", "", fields.Everything())

	// create the workqueue
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	// Bind the calico cache to kubernetes cache with the help of an informer. This way we make sure that
	// whenever the kubernetes cache is updated, changes get reflected in the Calico cache as well.
	indexer, informer := cache.NewIndexerInformer(listWatcher, &v1.Namespace{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			log.Info("Got ADD event for Namespace: %#v", obj)

			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
		UpdateFunc: func(old interface{}, new interface{}) {
			log.Info("Got UPDATE event for Namespace")
			log.Info("Old object: \n%#v\n", old)
			log.Info("New object: \n%#v\n", new)
			if new.(*v1.Namespace).Status.Phase == "Terminating" {
				// Ignore any updates with "Terminating" status, since
				// we will soon receive a DELETE event to remove this object.
				log.Debugf("Ignoring 'Terminating' update for Namespace %s.", new.(*v1.Namespace).ObjectMeta.GetName())
				return
			}

			key, err := cache.MetaNamespaceKeyFunc(new)
			if err == nil {
				queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			// Convert the namespace into a Profile.
			log.Info("Got DELETE event for namespace: %#v", obj)
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}

		},
	}, cache.Indexers{})

	return &namespaceController{indexer, informer, ctx, queue}
}

// Run starts the controller.
func (c *namespaceController) Run(threadiness int, reconcilerPeriod string, stopCh chan struct{}) {
	defer uruntime.HandleCrash()

	// Let the workers stop when we are done
	defer c.queue.ShutDown()

	log.Info("Starting Namespace/Profile controller")

	// Wait till k8s cache is synced
	go c.informer.Run(stopCh)
	log.Debug("Waiting to sync with Kubernetes API (Namespace/Profile)")

	for !c.informer.HasSynced() {
	}
	log.Debug("Finished syncing with Kubernetes API (Namespace/Profile)")

	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	// Start a number of worker threads to read from the queue.
	for i := 0; i < threadiness; i++ {
		go c.runWorker()
	}
	log.Info("Namespace/Profile controller is now running")

	<-stopCh
	log.Info("Stopping Namespace/Profile controller")
}

func (c *namespaceController) runWorker() {
	for c.processNextItem() {
	}
}

// processNextItem waits for an event on the output queue from the resource cache and syncs
// any received keys to the datastore.
func (c *namespaceController) processNextItem() bool {
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
func (c *namespaceController) syncToStdout(key string) error {
	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		klog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		// Below we will warm up our cache with a Pod, so that we will see a delete for one pod
		log.Info("Namespace %s does not exist anymore", key)
	} else {
		// Note that you also have to check the uid if you have a local controlled resource, which
		// is dependent on the actual instance, to detect that a Pod was recreated with the same name
		log.Info("Sync/Add/Update for Namespace %s", obj.(*v1.Namespace).GetName())
	}
	return nil
}

// handleErr handles errors which occur while processing a key received from the resource cache.
// For a given error, we will re-queue the key in order to retry the datastore sync up to 5 times,
// at which point the update is dropped.
func (c *namespaceController) handleErr(err error, key interface{}) {
	if err == nil {
		// Forget about the #AddRateLimited history of the key on every successful synchronization.
		// This ensures that future processing of updates for this key is not delayed because of
		// an outdated error history.
		c.queue.Forget(key)
		return
	}

	// This controller retries 5 times if something goes wrong. After that, it stops trying.
	if c.queue.NumRequeues(key) < 5 {
		// Re-enqueue the key rate limited. Based on the rate limiter on the
		// queue and the re-enqueue history, the key will be processed later again.
		log.WithError(err).Errorf("Error syncing Profile %v: %v", key, err)
		c.queue.AddRateLimited(key)
		return
	}
	c.queue.Forget(key)

	// Report to an external entity that, even after several retries, we could not successfully process this key
	uruntime.HandleError(err)
	log.WithError(err).Errorf("Dropping Profile %q out of the queue: %v", key, err)
}
