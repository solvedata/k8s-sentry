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
	"fmt"
	"log"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	lru "github.com/hashicorp/golang-lru"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type terminationKey struct {
	podUID        types.UID
	containerName string
}

type application struct {
	clientset          *kubernetes.Clientset
	defaultEnvironment string
	release            string
	namespace          string
	defaultTags        map[string]string
	terminationsSeen   *lru.Cache
}

func (app *application) Run() (chan struct{}, error) {
	terminationsSeen, err := lru.New(500)
	if err != nil {
		return nil, err
	}
	app.terminationsSeen = terminationsSeen
	if app.namespace == "" {
		app.namespace = v1.NamespaceAll
	}
	stop := make(chan struct{})
	go app.monitorEvents(stop)
	return stop, nil
}

func (app application) monitorEvents(stop chan struct{}) {
	watchList := cache.NewListWatchFromClient(
		app.clientset.CoreV1().RESTClient(),
		"events",
		app.namespace,
		fields.Everything(),
	)
	_, controller := cache.NewInformer(
		watchList,
		&v1.Event{},
		time.Second*30,
		cache.ResourceEventHandlerFuncs{
			AddFunc: app.handleEventAdd,
		},
	)

	controller.Run(stop)
}

func (app application) handleEventAdd(obj interface{}) {
	evt, ok := obj.(*v1.Event)
	if !ok {
		sentry.CaptureMessage("Unexpected event type")
		return
	}

	if skipEvent(evt) {
		return
	}

	sentryEvent := sentry.NewEvent()
	sentryEvent.Platform = "other"
	if app.defaultEnvironment != "" {
		sentryEvent.Environment = app.defaultEnvironment
	} else {
		sentryEvent.Environment = evt.InvolvedObject.Namespace
	}

	sentryEvent.Logger = "kubernetes"
	sentryEvent.Message = fmt.Sprintf("%s/%s: %s", evt.InvolvedObject.Kind, evt.InvolvedObject.Name, evt.Message)
	sentryEvent.Level = getSentryLevel(evt)
	sentryEvent.Timestamp = evt.ObjectMeta.CreationTimestamp.Unix()
	sentryEvent.Fingerprint = []string{
		evt.Source.Component,
		evt.Type,
		evt.Reason,
		evt.Message,
	}

	copyTags(sentryEvent, app.defaultTags)
	sentryEvent.Tags["namespace"] = evt.InvolvedObject.Namespace
	sentryEvent.Tags["component"] = evt.Source.Component
	if evt.ClusterName != "" {
		sentryEvent.Tags["cluster"] = evt.ClusterName
	}
	sentryEvent.Tags["reason"] = evt.Reason
	sentryEvent.Tags["kind"] = evt.InvolvedObject.Kind
	sentryEvent.Tags["type"] = evt.Type
	if evt.Action != "" {
		sentryEvent.Extra["action"] = evt.Action
	}
	sentryEvent.Extra["count"] = evt.Count

	handler := NewEventHandler(&app, evt)
	sentryEvent.Fingerprint = append(sentryEvent.Fingerprint, handler.Fingerprint()...)
	for k, v := range handler.Tags() {
		sentryEvent.Tags[k] = v
	}

	log.Printf("%s %s\n", evt.Type, sentryEvent.Message)
	sentry.CaptureEvent(sentryEvent)
}

func skipEvent(evt *v1.Event) bool {
	return evt.Type == v1.EventTypeNormal
}

func getSentryLevel(evt *v1.Event) sentry.Level {
	switch evt.Type {
	case v1.EventTypeWarning:
		return sentry.LevelWarning
	case "Error":
		return sentry.LevelError
	default:
		fmt.Printf("Unexpected event type: %v\n", evt.Type)
		return sentry.LevelInfo
	}
}

func getEventFingerprint(evt *v1.Event) []string {
	return []string{
		evt.Source.Component,
		evt.InvolvedObject.APIVersion,
		evt.InvolvedObject.Kind,
		evt.InvolvedObject.Namespace,
		evt.InvolvedObject.Name,
		evt.InvolvedObject.FieldPath,
		evt.Type,
		evt.Reason,
		evt.Message,
	}
}

func inCluster() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != "" && os.Getenv("KUBERNETES_SERVICE_PORT") != ""
}

func copyTags(event *sentry.Event, tags map[string]string) {
	for k, v := range tags {
		event.Tags[k] = v
	}
}
