// Copyright 2017 Google Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//     http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/istio/pilot/tools/version"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

const initializerName = "initializer.istio.io"

type config struct {
	enableCoreDump  bool
	hub             string
	includeIPRanges string
	istioSystem     string
	meshConfig      string
	sidecarProxyUID int64
	tag             string
	verbosity       int
	version         string
}

func main() {
	var kubeconfig *string
	kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	flag.Parse()

	log.Println("Starting the istio initializer...")
	log.Printf("Initializer name set to: %s", initializerName)

	kconfig, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		log.Fatal(err)
	}

	clientset, err := kubernetes.NewForConfig(kconfig)
	if err != nil {
		log.Fatal(err)
	}

	cm, err := clientset.CoreV1().ConfigMaps("default").Get("istio-initializer", metav1.GetOptions{})
	if err != nil {
		log.Fatal(err)
	}

	c, err := configmapToConfig(cm)
	if err != nil {
		log.Fatal(err)
	}

	watchlist := cache.NewListWatchFromClient(clientset.Core().RESTClient(), "pods", corev1.NamespaceAll, fields.Everything())

	includeUninitializedWatchlist := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.IncludeUninitialized = true
			return watchlist.List(options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.IncludeUninitialized = true
			return watchlist.Watch(options)
		},
	}

	resyncPeriod := 30 * time.Second

	_, controller := cache.NewInformer(includeUninitializedWatchlist, &corev1.Pod{}, resyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				err := initializePod(obj.(*corev1.Pod), c, clientset)
				if err != nil {
					log.Println(err)
				}
			},
		})

	stop := make(chan struct{})
	go controller.Run(stop)

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan

	log.Println("Shutdown signal received, exiting...")
	close(stop)
}

func initializePod(pod *corev1.Pod, c *config, clientset *kubernetes.Clientset) error {
	if pod.ObjectMeta.GetInitializers() != nil {
		pendingInitializers := pod.ObjectMeta.GetInitializers().Pending

		if initializerName == pendingInitializers[0].Name {
			log.Printf("initializing pod: %s", pod.Name)

			// Remove self from the list of pending Initializers while preserving ordering.
			if len(pendingInitializers) == 1 {
				pod.ObjectMeta.Initializers = nil
			} else {
				pod.ObjectMeta.Initializers.Pending = append(pendingInitializers[:0], pendingInitializers[1:]...)
			}

			// Modify the PodSec and post an update.
			_, err := clientset.CoreV1().Pods(pod.Namespace).Update(pod)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func configmapToConfig(c *corev1.ConfigMap) (*config, error) {
	var enableCoreDump bool
	var err error

	enableCoreDump, err = strconv.ParseBool(c.Data["enableCoreDump"])
	if err != nil {
		enableCoreDump = false
	}

	var sidecarProxyUID int64
	sidecarProxyUID, err = strconv.ParseInt(c.Data["sidecarProxyUID"], 10, 64)
	if err != nil {
		sidecarProxyUID = int64(1337)
	}

	var verbosity int
	verbosity, err = strconv.Atoi(c.Data["verbosity"])
	if err != nil {
		verbosity = 2
	}

	cfg := &config{
		enableCoreDump:  enableCoreDump,
		hub:             c.Data["hub"],
		includeIPRanges: c.Data["includeIPRanges"],
		istioSystem:     c.Data["istioSystem"],
		meshConfig:      c.Data["meshConfig"],
		sidecarProxyUID: sidecarProxyUID,
		tag:             c.Data["tag"],
		verbosity:       verbosity,
		version:         c.Data["version"],
	}

	if cfg.hub == "" {
		cfg.hub = "docker.io/istio"
	}

	if cfg.istioSystem == "" {
		cfg.istioSystem = "default"
	}

	if cfg.meshConfig == "" {
		cfg.meshConfig = "istio"
	}

	if cfg.tag == "" {
		cfg.tag = "0.1"
	}

	if cfg.version == "" {
		cfg.version = version.Line()
	}

	return cfg, nil
}
