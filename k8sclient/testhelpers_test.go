package k8sclient

import "k8s.io/apimachinery/pkg/runtime"

// toRuntimeObjects converts the loosely-typed fixtures the tests build into
// the runtime.Object slice the fake clientset seeds itself from.
func toRuntimeObjects(in []any) []runtime.Object {
	out := make([]runtime.Object, 0, len(in))
	for _, o := range in {
		if ro, ok := o.(runtime.Object); ok {
			out = append(out, ro)
		}
	}
	return out
}
