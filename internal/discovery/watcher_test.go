package discovery

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

func TestSandboxPodFromBackingPod(t *testing.T) {
	controller := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "session-a", Namespace: "agents", UID: types.UID("pod-uid"),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: sandboxv1beta1.GroupVersion.String(), Kind: "Sandbox",
				Name: "session-a", UID: types.UID("sandbox-uid"), Controller: &controller,
			}},
		},
		Spec:   corev1.PodSpec{NodeName: "worker-a"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	event, ok := SandboxPodFromPod(pod)
	if !ok {
		t.Fatal("expected Agent Sandbox backing Pod to be discovered")
	}
	if event.SandboxName != "session-a" || event.PodUID != "pod-uid" || !event.Runnable() {
		t.Fatalf("event = %#v", event)
	}
}

func TestWatcherPublishesSandboxPod(t *testing.T) {
	pod := backingPod()
	client := fake.NewClientset()
	fakeWatch := watch.NewRaceFreeFake()
	client.PrependWatchReactor("pods", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatch, nil
	})
	watcher, err := NewWatcher(client, "worker-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for !hasWatchAction(client.Actions()) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !hasWatchAction(client.Actions()) {
		t.Fatal("Pod informer did not establish a watch")
	}
	fakeWatch.Add(pod)
	fakeWatch.Action(watch.Bookmark, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		ResourceVersion: "1", Annotations: map[string]string{metav1.InitialEventsAnnotationKey: "true"},
	}})

	select {
	case event := <-watcher.Events():
		if event.PodUID != "pod-uid" || event.SandboxUID != "sandbox-uid" || !event.Runnable() {
			t.Fatalf("event = %#v", event)
		}
	case err := <-done:
		t.Fatalf("watcher stopped before publishing event: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial Sandbox Pod event")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not stop with its context")
	}
}

func hasWatchAction(actions []k8stesting.Action) bool {
	for _, action := range actions {
		if action.GetVerb() == "watch" && action.GetResource().Resource == "pods" {
			return true
		}
	}
	return false
}

func TestSandboxPodFromPodRejectsOrdinaryWorkload(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "deployment-pod", UID: types.UID("pod-uid")},
		Spec:       corev1.PodSpec{NodeName: "worker-a"},
	}
	if _, ok := SandboxPodFromPod(pod); ok {
		t.Fatal("ordinary Pod must not become an AgentRM scheduling entity")
	}
}

func backingPod() *corev1.Pod {
	controller := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "session-a", Namespace: "agents", UID: types.UID("pod-uid"),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: sandboxv1beta1.GroupVersion.String(), Kind: "Sandbox",
				Name: "session-a", UID: types.UID("sandbox-uid"), Controller: &controller,
			}},
		},
		Spec:   corev1.PodSpec{NodeName: "worker-a"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}
