/*
Copyright 2019 Wichert Akkerman

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
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var configFlag = flag.String("kubeconfig", "", "Configuration file")
var defaultEnvironment = os.Getenv("ENVIRONMENT")
var release = os.Getenv("RELEASE")
var defaultTags = os.Getenv("TAGS")

func main() {
	flag.Parse()

	if os.Getenv("SENTRY_DSN") == "" {
		log.Println("Warning: SENTRY_DSN environment variable not set. Can not report to Sentry")
	}

	tags, err := parseTags(defaultTags)
	if err != nil {
		log.Fatalf("Error parsing default tags: %v", err)
	}

	err = sentry.Init(sentry.ClientOptions{
		Environment: defaultEnvironment,
		Release:     release,
	})
	if err != nil {
		log.Fatalf("Error initialising sentry: %v", err)
	}

	clientset, err := createKubernetesClient(*configFlag)
	if err != nil {
		sentry.CaptureException(err)
		log.Fatalf("Error creating kubernetes client: %v", err)
	}

	app := application{
		clientset:          clientset,
		defaultEnvironment: os.Getenv("ENVIRONMENT"),
		namespace:          os.Getenv("NAMESPACE"),
		defaultTags:        tags,
	}

	stopSignal, err := app.Run()
	if err != nil {
		sentry.CaptureException(err)
		log.Fatalf("Error starting monitors: %v", err)
	}
	abortSignal := make(chan os.Signal)
	signal.Notify(abortSignal, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
	<-abortSignal

	stopSignal <- struct{}{}
	log.Println("Exiting")
	// Make sure all events are flushed before we terminate
	sentry.Flush(time.Second * 1)
}

func createKubernetesClient(configFile string) (client *kubernetes.Clientset, err error) {
	var config *rest.Config
	if configFile == "" && !inCluster() {
		// If we are not running in a cluster default to reading ~/.kube/config
		if usr, err := user.Current(); err == nil {
			configFile = filepath.Join(usr.HomeDir, ".kube", "config")
		}
	}

	if configFile == "" {
		config, err = rest.InClusterConfig()
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", configFile)
	}
	if err != nil {
		return
	}
	return kubernetes.NewForConfig(config)
}

func parseTags(tags string) (map[string]string, error) {
	result := make(map[string]string)
	for _, tag := range strings.Split(tags, ",") {
		keyValue := strings.Split(tag, "=")
		if len(keyValue) != 2 {
			return nil, fmt.Errorf("invalid tag '%s', expected key=value pair", tag)
		}

		key := keyValue[0]
		value := keyValue[1]

		result[key] = value
	}

	return result, nil
}
