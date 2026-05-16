package k8slease

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// newFakeClient returns a kubernetes.Interface backed by client-go's
// in-memory fake clientset. The unit tests use it purely as a non-nil
// argument to [New]; none of them call [Elector.Run], which is the only
// surface that would actually exercise the client.
//
// The integration tests in ./integrationtest exercise the full
// LeaderElector loop against the same fake clientset.
func newFakeClient() kubernetes.Interface {
	return fake.NewClientset()
}
