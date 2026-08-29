package discovery

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

func TestSandboxPodFromPodRejectsOrdinaryWorkload(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "deployment-pod", UID: types.UID("pod-uid")},
		Spec:       corev1.PodSpec{NodeName: "worker-a"},
	}
	if _, ok := SandboxPodFromPod(pod); ok {
		t.Fatal("ordinary Pod must not become an AgentRM scheduling entity")
	}
}
