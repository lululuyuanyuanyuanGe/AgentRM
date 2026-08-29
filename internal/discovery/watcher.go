package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

type EventType string

const (
	EventUpsert EventType = "UPSERT"
	EventDelete EventType = "DELETE"
)

// SandboxPod is the minimal identity needed to bind one Agent Sandbox Pod to
// its local cgroup. It deliberately carries no tool or command semantics.
type SandboxPod struct {
	Type        EventType
	Namespace   string
	SandboxName string
	SandboxUID  string
	PodName     string
	PodUID      string
	NodeName    string
	Phase       corev1.PodPhase
}

func (p SandboxPod) Runnable() bool {
	return p.Type == EventUpsert && p.Phase == corev1.PodRunning
}

type Watcher struct {
	client   kubernetes.Interface
	nodeName string
	logger   *slog.Logger
	events   chan SandboxPod
}

func NewWatcher(client kubernetes.Interface, nodeName string, logger *slog.Logger) (*Watcher, error) {
	if client == nil {
		return nil, errors.New("kubernetes client is required")
	}
	if nodeName == "" {
		return nil, errors.New("node name is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Watcher{client: client, nodeName: nodeName, logger: logger, events: make(chan SandboxPod, 1024)}, nil
}

func (w *Watcher) Events() <-chan SandboxPod { return w.events }

func (w *Watcher) Run(ctx context.Context) error {
	selector := fields.OneTermEqualSelector("spec.nodeName", w.nodeName).String()
	listWatch := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.FieldSelector = selector
			return w.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = selector
			return w.client.CoreV1().Pods(metav1.NamespaceAll).Watch(ctx, options)
		},
	}
	informer := cache.NewSharedIndexInformer(listWatch, &corev1.Pod{}, 0, cache.Indexers{})
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) { w.publish(ctx, EventUpsert, obj) },
		UpdateFunc: func(_, current any) {
			w.publish(ctx, EventUpsert, current)
		},
		DeleteFunc: func(obj any) { w.publish(ctx, EventDelete, obj) },
	}); err != nil {
		return fmt.Errorf("add pod event handler: %w", err)
	}

	go informer.Run(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("agent sandbox pod cache did not sync")
	}
	<-ctx.Done()
	return ctx.Err()
}

func (w *Watcher) publish(ctx context.Context, eventType EventType, obj any) {
	pod, ok := podFromObject(obj)
	if !ok {
		w.logger.Warn("ignored unknown pod watch object")
		return
	}
	event, ok := SandboxPodFromPod(pod)
	if !ok {
		return
	}
	event.Type = eventType
	if pod.DeletionTimestamp != nil {
		event.Type = EventDelete
	}
	select {
	case w.events <- event:
	case <-ctx.Done():
	}
}

func podFromObject(obj any) (*corev1.Pod, bool) {
	if pod, ok := obj.(*corev1.Pod); ok {
		return pod, true
	}
	tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
	if !ok {
		return nil, false
	}
	pod, ok := tombstone.Obj.(*corev1.Pod)
	return pod, ok
}

func SandboxPodFromPod(pod *corev1.Pod) (SandboxPod, bool) {
	if pod == nil || pod.UID == "" || pod.Spec.NodeName == "" {
		return SandboxPod{}, false
	}
	for _, owner := range pod.OwnerReferences {
		if owner.APIVersion != sandboxv1beta1.GroupVersion.String() || owner.Kind != "Sandbox" || owner.UID == "" {
			continue
		}
		if owner.Controller != nil && !*owner.Controller {
			continue
		}
		return SandboxPod{
			Type: EventUpsert, Namespace: pod.Namespace, SandboxName: owner.Name, SandboxUID: string(owner.UID),
			PodName: pod.Name, PodUID: string(pod.UID), NodeName: pod.Spec.NodeName, Phase: pod.Status.Phase,
		}, true
	}
	return SandboxPod{}, false
}
