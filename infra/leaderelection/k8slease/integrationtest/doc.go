// Package integrationtest holds the fake-clientset-backed integration
// scenarios for infra/leaderelection/k8slease.
//
// We use [k8s.io/client-go/kubernetes/fake] rather than testcontainers
// because spinning up a real apiserver (kind, k3d, kwok) for one
// leader-election adapter would balloon CI cost without exercising
// anything the fake clientset's reactor doesn't already cover: the
// client-go LeaderElector talks to the Lease object via the standard
// CoordinationV1 client interface, which the fake clientset satisfies
// faithfully via its tracker. Real-apiserver integration is exercised
// at the consumer level (apps using this adapter run on a real
// cluster); the kit's responsibility is to assert the adapter wires
// the kit contract onto client-go correctly.
package integrationtest
